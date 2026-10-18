//go:build live

package regruzone

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

var liveAllowedTypes = map[zone.Type]bool{
	zone.A: true, zone.AAAA: true, zone.CNAME: true, zone.MX: true,
	zone.TXT: true, zone.NS: true, zone.SRV: true, zone.CAA: true,
}

type liveEnv struct {
	zone     string
	username string
	password string
	write    bool
}

func loadLiveEnv(t *testing.T, needWrite bool) liveEnv {
	t.Helper()
	e := liveEnv{
		zone:     os.Getenv("REGRU_LIVE_ZONE"),
		username: os.Getenv("REGRU_USERNAME"),
		password: os.Getenv("REGRU_PASSWORD"),
		write:    os.Getenv("REGRU_LIVE_ALLOW_WRITE") == "1",
	}
	if e.zone == "" || e.username == "" || e.password == "" {
		t.Skip("live test disabled: REGRU_LIVE_ZONE, REGRU_USERNAME and REGRU_PASSWORD are required")
	}
	if needWrite && !e.write {
		t.Skip("live write test disabled: REGRU_LIVE_ALLOW_WRITE=1 is required")
	}
	return e
}

func newLiveClient(e liveEnv) *regruapi.Client {
	lim := regruapi.NewLimiter(3*time.Second, nil)
	return regruapi.New(regruapi.Credentials{Username: e.username, Password: e.password}, lim)
}

func randomRunID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("run id: %v", err)
	}
	return hex.EncodeToString(b)
}

type liveHarness struct {
	t      *testing.T
	ctx    context.Context
	c      *regruapi.Client
	b      *Backend
	zone   string
	prefix string
	owners map[string]bool
	halted bool
	unsure []string
}

func (h *liveHarness) allowed(r zone.Record) error {
	if !h.owners[r.Name] {
		return fmt.Errorf("owner %q is not in the allowlist", r.Name)
	}
	if !strings.Contains(r.Name, h.prefix) {
		return fmt.Errorf("owner %q is outside the run prefix", r.Name)
	}
	if !liveAllowedTypes[r.Type] {
		return fmt.Errorf("type %s is not in the allowlist", r.Type)
	}
	if r.Type == zone.NS && r.Name == "@" {
		return errors.New("apex NS is protected")
	}
	return nil
}

func (h *liveHarness) mutate(op string, r zone.Record) error {
	if h.halted {
		return errors.New("mutations are halted after an unknown outcome")
	}
	if err := h.allowed(r); err != nil {
		return err
	}
	var err error
	switch op {
	case "add":
		err = h.b.Add(h.ctx, r)
	case "remove":
		err = h.b.Remove(h.ctx, r)
	default:
		return fmt.Errorf("unknown op %q", op)
	}
	if err != nil {
		var u interface{ UnknownOutcome() bool }
		if errors.As(err, &u) && u.UnknownOutcome() {
			h.halted = true
			h.unsure = append(h.unsure, fmt.Sprintf("%s %s %s %q", op, r.Name, r.Type, r.Value))
		}
		return fmt.Errorf("%s %s %s: %w", op, r.Name, r.Type, err)
	}
	return nil
}

func (h *liveHarness) snapshot() (zone.Snapshot, error) {
	s, _, err := h.b.Snapshot(h.ctx)
	return s, err
}

func (h *liveHarness) under(s zone.Snapshot, owner string) []zone.Record {
	var out []zone.Record
	for _, r := range s.Records {
		if r.Name == owner {
			out = append(out, r)
		}
	}
	return out
}

func (h *liveHarness) leftovers(s zone.Snapshot) []zone.Record {
	var out []zone.Record
	for _, r := range s.Records {
		if strings.Contains(r.Name, h.prefix) {
			out = append(out, r)
		}
	}
	return out
}

func has(rs []zone.Record, r zone.Record) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

func (h *liveHarness) cleanupOwner(owner string) {
	for i := 0; i < 6; i++ {
		if h.halted {
			return
		}
		s, err := h.snapshot()
		if err != nil {
			h.t.Errorf("cleanup snapshot: %v", err)
			return
		}
		rs := h.under(s, owner)
		if len(rs) == 0 {
			return
		}
		if err := h.mutate("remove", rs[0]); err != nil {
			h.t.Errorf("cleanup: %v", err)
			return
		}
	}
}

func (h *liveHarness) expect(owner string, present, absent []zone.Record) error {
	s, err := h.snapshot()
	if err != nil {
		return err
	}
	rs := h.under(s, owner)
	for _, r := range present {
		if !has(rs, r) {
			return fmt.Errorf("record %s %s %q is missing; observed %v", r.Name, r.Type, r.Value, rs)
		}
	}
	for _, r := range absent {
		if has(rs, r) {
			return fmt.Errorf("record %s %s %q is still present", r.Name, r.Type, r.Value)
		}
	}
	if len(rs) != len(present) {
		return fmt.Errorf("owner %s has %d records, want %d: %v", owner, len(rs), len(present), rs)
	}
	return nil
}

func (h *liveHarness) subcase(name, owner string, steps func() error) {
	h.t.Run(name, func(t *testing.T) {
		if h.halted {
			t.Skip("mutations halted after an unknown outcome")
		}
		h.owners[owner] = true
		err := steps()
		if err != nil {
			t.Errorf("%v", err)
		}
		if h.halted {
			t.Errorf("unknown outcome, no further mutations")
			return
		}
		h.cleanupOwner(owner)
		if err := h.expect(owner, nil, nil); err != nil {
			t.Errorf("cleanup not verified: %v", err)
		}
	})
}

func newLiveHarness(t *testing.T, e liveEnv) *liveHarness {
	t.Helper()
	zn, err := zone.NormalizeZone(e.zone)
	if err != nil {
		t.Fatalf("zone: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	t.Cleanup(cancel)
	c := newLiveClient(e)
	run := randomRunID(t)
	return &liveHarness{
		t: t, ctx: ctx, c: c, b: NewBackend(c, zn), zone: zn,
		prefix: "regru-live-" + run,
		owners: map[string]bool{},
	}
}

func (h *liveHarness) preflight() zone.Snapshot {
	t := h.t
	t.Helper()
	id, err := h.b.Identity(h.ctx)
	if err != nil || id == "" {
		t.Fatalf("identity: %q %v", id, err)
	}
	nss, err := h.c.GetNSS(h.ctx, []string{h.zone})
	if err != nil {
		t.Fatalf("get_nss: %v", err)
	}
	if len(nss) != 1 || nss[0].Err != nil {
		t.Fatalf("get_nss: unexpected result %+v", nss)
	}
	got := map[string]bool{}
	for _, n := range nss[0].NSS {
		got[strings.TrimSuffix(strings.ToLower(n), ".")] = true
	}
	if !got["ns1.reg.ru"] || !got["ns2.reg.ru"] {
		t.Fatalf("zone is not served by ns1/ns2.reg.ru: %v", nss[0].NSS)
	}
	base, err := h.snapshot()
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if left := h.leftovers(base); len(left) != 0 {
		t.Fatalf("names under prefix %s already exist: %v", h.prefix, left)
	}
	return base.Sorted()
}

func (h *liveHarness) finish(base zone.Snapshot) {
	t := h.t
	t.Helper()
	s, err := h.snapshot()
	if err != nil {
		t.Errorf("final snapshot: %v", err)
		return
	}
	if left := h.leftovers(s); len(left) != 0 {
		t.Errorf("leftover records under %s: %v", h.prefix, left)
	}
	if len(h.unsure) != 0 {
		t.Errorf("operations with unknown outcome: %v", h.unsure)
	}
	fin := s.Sorted()
	if !reflect.DeepEqual(base.Records, fin.Records) {
		t.Errorf("zone differs from baseline:\nbaseline: %v\nfinal: %v", base.Records, fin.Records)
	}
	if !reflect.DeepEqual(base.SOA, fin.SOA) {
		t.Errorf("SOA differs from baseline: %v vs %v", base.SOA, fin.SOA)
	}
}

func liveCanon(t *testing.T, typ zone.Type, v string) string {
	t.Helper()
	c, err := zone.ParseValue(typ, v)
	if err != nil {
		t.Fatalf("value %q: %v", v, err)
	}
	return c
}

func TestLiveExactRemove(t *testing.T) {
	e := loadLiveEnv(t, true)
	h := newLiveHarness(t, e)
	base := h.preflight()
	defer h.finish(base)

	type pair struct {
		typ     zone.Type
		owner   string
		target  string
		control string
	}
	pairs := []pair{
		{zone.A, h.prefix + "-a", "192.0.2.10", "192.0.2.11"},
		{zone.AAAA, h.prefix + "-aaaa", "2001:db8::10", "2001:db8::11"},
		{zone.MX, h.prefix + "-mx", "10 mx-a.example.net.", "20 mx-a.example.net."},
		{zone.TXT, h.prefix + "-txt", "regru live target", "regru live control"},
		{zone.NS, h.prefix + "-ns", "ns-a.example.net.", "ns-b.example.net."},
		{zone.SRV, "_s1._tcp." + h.prefix + "-srv", "10 5 5060 sip.example.net.", "10 6 5060 sip.example.net."},
		{zone.CAA, h.prefix + "-caa", "0 issue example.net", "0 issue example.org"},
	}
	for _, p := range pairs {
		target := zone.Record{Name: p.owner, Type: p.typ, Value: liveCanon(t, p.typ, p.target)}
		control := zone.Record{Name: p.owner, Type: p.typ, Value: liveCanon(t, p.typ, p.control)}
		h.subcase(string(p.typ), p.owner, func() error {
			if err := h.expect(p.owner, nil, nil); err != nil {
				return fmt.Errorf("owner is not empty before the case: %w", err)
			}
			if err := h.mutate("add", target); err != nil {
				return err
			}
			if err := h.mutate("add", control); err != nil {
				return err
			}
			if err := h.expect(p.owner, []zone.Record{target, control}, nil); err != nil {
				return fmt.Errorf("after add: %w", err)
			}
			if err := h.mutate("remove", target); err != nil {
				return err
			}
			if err := h.expect(p.owner, []zone.Record{control}, []zone.Record{target}); err != nil {
				return fmt.Errorf("after exact remove: %w", err)
			}
			return nil
		})
	}

	cname := h.prefix + "-cname"
	t1 := zone.Record{Name: cname, Type: zone.CNAME, Value: liveCanon(t, zone.CNAME, "t1.example.net.")}
	t2 := zone.Record{Name: cname, Type: zone.CNAME, Value: liveCanon(t, zone.CNAME, "t2.example.net.")}
	h.subcase("CNAME", cname, func() error {
		if err := h.expect(cname, nil, nil); err != nil {
			return fmt.Errorf("owner is not empty before the case: %w", err)
		}
		if err := h.mutate("add", t1); err != nil {
			return err
		}
		if err := h.expect(cname, []zone.Record{t1}, nil); err != nil {
			return fmt.Errorf("after add: %w", err)
		}
		if err := h.mutate("remove", t1); err != nil {
			return err
		}
		if err := h.expect(cname, nil, []zone.Record{t1}); err != nil {
			return fmt.Errorf("after remove: %w", err)
		}
		if err := h.mutate("add", t2); err != nil {
			return err
		}
		if err := h.expect(cname, []zone.Record{t2}, []zone.Record{t1}); err != nil {
			return fmt.Errorf("after target change: %w", err)
		}
		return nil
	})
}

func TestLiveListDomainsComplete(t *testing.T) {
	e := loadLiveEnv(t, false)
	c := newLiveClient(e)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	svcs, err := c.ListDomains(ctx)
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	t.Logf("domains listed: %d", len(svcs))
	raw := os.Getenv("REGRU_LIVE_EXPECT_DOMAINS")
	if raw == "" {
		return
	}
	want, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("REGRU_LIVE_EXPECT_DOMAINS: %v", err)
	}
	if len(svcs) != want {
		t.Fatalf("ListDomains returned %d domains, want %d", len(svcs), want)
	}
}
