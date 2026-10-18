package plan

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lavr/regru-cli/internal/zone"
)

func planRules() Rules {
	rw := zone.Cap{Read: true, Write: true, ExactDelete: true}
	return Rules{
		Caps: map[zone.Type]zone.Cap{
			zone.A: rw, zone.AAAA: rw, zone.CNAME: rw, zone.MX: rw,
			zone.TXT: rw, zone.NS: rw, zone.SRV: rw, zone.CAA: rw,
			zone.HTTPS: {Read: true},
		},
		Protected: func(k zone.Key) bool { return k.Type == zone.NS && k.Name == "@" },
		ValidateWrite: func(r zone.Record) error {
			if r.Type != zone.CAA {
				return nil
			}
			f := strings.Fields(r.Value)
			if len(f) < 2 {
				return errors.New("bad caa")
			}
			switch f[1] {
			case "issue", "issuewild", "iodef":
				return nil
			}
			return errors.New("unsupported caa tag")
		},
	}
}

func pol() Policy {
	return Policy{MaxDeletes: 100, Rules: planRules()}
}

func rec(name string, t zone.Type, v string) zone.Record {
	return zone.Record{Name: name, Type: t, Value: v}
}

func key(name string, t zone.Type) zone.Key {
	return zone.Key{Name: name, Type: t}
}

func snap(recs ...zone.Record) zone.Snapshot {
	return zone.Snapshot{Zone: "example.test", Records: recs}
}

func listed(kv map[zone.Key][]string) Desired {
	return Desired{Scope: ScopeListed, RRsets: kv}
}

func managed(kv map[zone.Key][]string) Desired {
	return Desired{Scope: ScopeManaged, RRsets: kv}
}

func policyCode(t *testing.T, err error) string {
	t.Helper()
	var pe *PolicyError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PolicyError, got %T %v", err, err)
	}
	return pe.Code
}

func wantPolicy(t *testing.T, p Plan, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got plan %+v", code, p.Ops)
	}
	if got := policyCode(t, err); got != code {
		t.Fatalf("code = %s, want %s (%v)", got, code, err)
	}
	if len(p.Ops) != 0 {
		t.Fatalf("ops must be empty on error: %+v", p.Ops)
	}
}

func mustBuild(t *testing.T, cur zone.Snapshot, want Desired, p Policy) Plan {
	t.Helper()
	pl, err := Build(cur, want, p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return pl
}

func op(seq int, kind OpKind, r zone.Record, deps ...int) Op {
	return Op{Seq: seq, Kind: kind, Record: r, DependsOn: deps}
}

func checkOps(t *testing.T, got []Op, want ...Op) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ops = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Seq != w.Seq || g.Kind != w.Kind || g.Record != w.Record || !reflect.DeepEqual(append([]int{}, g.DependsOn...), append([]int{}, w.DependsOn...)) {
			t.Fatalf("op[%d] = %+v, want %+v (all %+v)", i, g, w, got)
		}
	}
}

func exportDesired(t *testing.T, s zone.Snapshot, scope Scope) Desired {
	t.Helper()
	b, err := ExportManifest("acc", s, scope, planRules())
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(b)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, b)
	}
	return m.Desired
}

func richSnapshot() zone.Snapshot {
	one := 1
	return zone.Snapshot{
		Zone: "example.test",
		Records: []zone.Record{
			rec("@", zone.NS, "ns1.example.net."),
			rec("@", zone.NS, "ns2.example.net."),
			rec("@", zone.A, "192.0.2.10"),
			rec("www", zone.A, "192.0.2.1"),
			rec("www", zone.A, "192.0.2.2"),
			rec("@", zone.MX, "10 mx.example.net."),
			rec("@", zone.TXT, `v=spf1 include:example.net "quoted" \ ~all`),
			rec("mail", zone.CNAME, "mx.example.net."),
			rec("h", zone.HTTPS, "1 . alpn=h2"),
		},
		Raw: []zone.Raw{
			{Name: "x", Type: "HTTPS", Content: "1 . alpn=h3", Prio: &one, Count: 1},
		},
		SOA: &zone.SOA{TTL: 86400, MinimumTTL: 10800},
	}
}

func TestNoopAfterExport(t *testing.T) {
	for _, scope := range []Scope{ScopeListed, ScopeManaged} {
		s := richSnapshot()
		p := mustBuild(t, s, exportDesired(t, s, scope), pol())
		if !p.Empty() || len(p.Warnings) != 0 {
			t.Fatalf("%s: expected empty plan, got %+v %v", scope, p.Ops, p.Warnings)
		}
		s.Raw = append(s.Raw, zone.Raw{Name: "dup", Type: "A", Content: "192.0.2.50", Count: 2})
		p = mustBuild(t, s, exportDesired(t, s, scope), pol())
		if !p.Empty() {
			t.Fatalf("%s with raw dup: expected empty plan, got %+v", scope, p.Ops)
		}
	}
}

func TestTargetIsFullState(t *testing.T) {
	cur := richSnapshot()
	orig := richSnapshot()
	p := mustBuild(t, cur, listed(map[zone.Key][]string{key("www", zone.A): {"192.0.2.3"}}), pol())
	if !reflect.DeepEqual(p.Initial, orig) {
		t.Fatalf("Initial = %+v, want %+v", p.Initial, orig)
	}
	p.Initial.Records[0].Value = "changed"
	p.Initial.SOA.TTL = 1
	if !reflect.DeepEqual(cur, orig) {
		t.Fatal("Initial aliases input snapshot")
	}
	has := func(r zone.Record) bool {
		for _, x := range p.Target.Records {
			if x == r {
				return true
			}
		}
		return false
	}
	for _, r := range []zone.Record{
		rec("@", zone.NS, "ns1.example.net."), rec("@", zone.NS, "ns2.example.net."),
		rec("h", zone.HTTPS, "1 . alpn=h2"), rec("www", zone.A, "192.0.2.3"), rec("@", zone.A, "192.0.2.10"),
	} {
		if !has(r) {
			t.Fatalf("target misses %+v: %+v", r, p.Target.Records)
		}
	}
	if has(rec("www", zone.A, "192.0.2.1")) || has(rec("www", zone.A, "192.0.2.2")) {
		t.Fatalf("target keeps replaced records: %+v", p.Target.Records)
	}
	if !reflect.DeepEqual(p.Target.Raw, orig.Raw) || !reflect.DeepEqual(p.Target.SOA, orig.SOA) || p.Target.Zone != orig.Zone {
		t.Fatalf("target raw/soa/zone differ: %+v", p.Target)
	}
	if len(p.Target.Records) != len(orig.Records)-1 {
		t.Fatalf("target size %d", len(p.Target.Records))
	}
}

func TestSetReplacesRRset(t *testing.T) {
	cur := snap(rec("www", zone.A, "192.0.2.1"), rec("www", zone.A, "192.0.2.2"))
	d, err := DesiredSet(key("www", zone.A), []string{"192.0.2.2", "192.0.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	p := mustBuild(t, cur, d, pol())
	checkOps(t, p.Ops,
		op(1, OpAdd, rec("www", zone.A, "192.0.2.3")),
		op(2, OpRemove, rec("www", zone.A, "192.0.2.1"), 1),
	)
	if p.Deletes() != 1 || p.Empty() || len(p.Warnings) != 0 {
		t.Fatalf("deletes=%d empty=%v warnings=%v", p.Deletes(), p.Empty(), p.Warnings)
	}
}

func TestDesiredSetEmpty(t *testing.T) {
	_, err := DesiredSet(key("www", zone.A), nil)
	if codeOf(err) != "invalid_value" {
		t.Fatalf("err = %v", err)
	}
}

func TestDesiredAddRemove(t *testing.T) {
	cur := snap(rec("www", zone.A, "192.0.2.1"), rec("www", zone.A, "192.0.2.2"), rec("x", zone.A, "192.0.2.9"))
	k := key("www", zone.A)
	p := mustBuild(t, cur, DesiredAdd(cur, k, []string{"192.0.2.3", "192.0.2.1"}), pol())
	checkOps(t, p.Ops, op(1, OpAdd, rec("www", zone.A, "192.0.2.3")))
	p = mustBuild(t, cur, DesiredRemove(cur, k, []string{"192.0.2.1"}, false), pol())
	checkOps(t, p.Ops, op(1, OpRemove, rec("www", zone.A, "192.0.2.1")))
	p = mustBuild(t, cur, DesiredRemove(cur, k, nil, true), pol())
	checkOps(t, p.Ops,
		op(1, OpRemove, rec("www", zone.A, "192.0.2.1")),
		op(2, OpRemove, rec("www", zone.A, "192.0.2.2")),
	)
}

func TestListedScopeLeavesOthers(t *testing.T) {
	cur := snap(rec("www", zone.A, "192.0.2.1"), rec("old", zone.A, "192.0.2.7"), rec("@", zone.MX, "10 mx.example.net."))
	p := mustBuild(t, cur, listed(map[zone.Key][]string{key("new", zone.A): {"192.0.2.8"}}), pol())
	checkOps(t, p.Ops, op(1, OpAdd, rec("new", zone.A, "192.0.2.8")))
}

func TestManagedScopeDeletes(t *testing.T) {
	cur := snap(
		rec("@", zone.NS, "ns1.example.net."),
		rec("www", zone.A, "192.0.2.1"),
		rec("old", zone.A, "192.0.2.7"),
		rec("@", zone.MX, "10 mx.example.net."),
		rec("h", zone.HTTPS, "1 . alpn=h2"),
	)
	p := mustBuild(t, cur, managed(map[zone.Key][]string{key("www", zone.A): {"192.0.2.1"}}), pol())
	checkOps(t, p.Ops,
		op(1, OpRemove, rec("@", zone.MX, "10 mx.example.net.")),
		op(2, OpRemove, rec("old", zone.A, "192.0.2.7")),
	)
	for _, o := range p.Ops {
		if o.Record.Type == zone.NS || o.Record.Type == zone.HTTPS {
			t.Fatalf("touched readonly/protected: %+v", o)
		}
	}
}

func TestProtectedViaDesiredSet(t *testing.T) {
	d, err := DesiredSet(key("@", zone.NS), []string{"ns.example.net."})
	if err != nil {
		t.Fatal(err)
	}
	for _, cur := range []zone.Snapshot{snap(rec("@", zone.NS, "ns1.example.net.")), snap()} {
		p, err := Build(cur, d, pol())
		wantPolicy(t, p, err, "protected_record")
	}
}

func TestProtectedEmptyValues(t *testing.T) {
	cur := snap(rec("@", zone.NS, "ns1.example.net."), rec("www", zone.A, "192.0.2.1"))
	p, err := Build(cur, listed(map[zone.Key][]string{key("@", zone.NS): {}}), pol())
	wantPolicy(t, p, err, "protected_record")
}

func TestUnsupportedWrite(t *testing.T) {
	d, err := DesiredSet(key("x", zone.HTTPS), []string{"1 . alpn=h2"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(snap(), d, pol())
	wantPolicy(t, p, err, "unsupported_type")
	d, err = DesiredSet(key("@", zone.CAA), []string{"0 foo a.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = Build(snap(), d, pol())
	wantPolicy(t, p, err, "unsupported_value")
	d, _ = DesiredSet(key("@", zone.CAA), []string{"0 issue a.example.net"})
	mustBuild(t, snap(), d, pol())
}

func TestRawOnlyCollision(t *testing.T) {
	cur := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{{Name: "x", Type: "A", Content: "192.0.2.1", Count: 2}}}
	p, err := Build(cur, listed(map[zone.Key][]string{key("x", zone.A): {"192.0.2.5"}}), pol())
	wantPolicy(t, p, err, "raw_conflict")
}

func TestEmptyZoneByValues(t *testing.T) {
	cur := snap(rec("@", zone.NS, "ns1.example.net."), rec("www", zone.A, "192.0.2.1"), rec("@", zone.MX, "10 mx.example.net."))
	d := managed(map[zone.Key][]string{key("www", zone.A): {}, key("@", zone.MX): {}})
	p, err := Build(cur, d, pol())
	wantPolicy(t, p, err, "empty_zone_not_allowed")
	allow := pol()
	allow.AllowEmptyZone = true
	p = mustBuild(t, cur, d, allow)
	if p.Deletes() != 2 {
		t.Fatalf("deletes = %d", p.Deletes())
	}
	cur.Raw = []zone.Raw{{Name: "dup", Type: "A", Content: "192.0.2.50", Count: 2}}
	p = mustBuild(t, cur, d, pol())
	if p.Deletes() != 2 {
		t.Fatalf("deletes with raw = %d", p.Deletes())
	}
	cur.Raw = []zone.Raw{{Name: "x", Type: "HTTPS", Content: "1 . alpn=h3", Count: 1}}
	p, err = Build(cur, d, pol())
	wantPolicy(t, p, err, "empty_zone_not_allowed")
}

func TestMaxDeletes(t *testing.T) {
	cur := snap(rec("a", zone.A, "192.0.2.1"), rec("b", zone.A, "192.0.2.2"), rec("c", zone.A, "192.0.2.3"), rec("d", zone.A, "192.0.2.4"))
	d := listed(map[zone.Key][]string{key("a", zone.A): {}, key("b", zone.A): {}, key("c", zone.A): {}})
	p := pol()
	p.MaxDeletes = 2
	pl, err := Build(cur, d, p)
	wantPolicy(t, pl, err, "max_deletes_exceeded")
	p.MaxDeletes = 3
	pl = mustBuild(t, cur, d, p)
	if pl.Deletes() != 3 {
		t.Fatalf("deletes = %d", pl.Deletes())
	}
}

func TestUnsupportedExactDelete(t *testing.T) {
	r := pol()
	caps := map[zone.Type]zone.Cap{}
	for k, v := range r.Rules.Caps {
		caps[k] = v
	}
	caps[zone.TXT] = zone.Cap{Read: true, Write: true}
	r.Rules.Caps = caps
	cur := snap(rec("t", zone.TXT, "a"), rec("www", zone.A, "192.0.2.1"))
	p, err := Build(cur, listed(map[zone.Key][]string{key("t", zone.TXT): {"b"}}), r)
	wantPolicy(t, p, err, "unsupported_exact_delete")
	mustBuild(t, cur, listed(map[zone.Key][]string{key("t", zone.TXT): {"a", "b"}}), r)
}

func TestCnameNeedsExplicitScope(t *testing.T) {
	cur := snap(rec("www", zone.A, "192.0.2.1"))
	p, err := Build(cur, listed(map[zone.Key][]string{key("www", zone.CNAME): {"t.example.net."}}), pol())
	wantPolicy(t, p, err, "scope_conflict")
	p = mustBuild(t, cur, listed(map[zone.Key][]string{
		key("www", zone.CNAME): {"t.example.net."},
		key("www", zone.A):     {},
	}), pol())
	checkOps(t, p.Ops,
		op(1, OpRemove, rec("www", zone.A, "192.0.2.1")),
		op(2, OpAdd, rec("www", zone.CNAME, "t.example.net."), 1),
	)
	if len(p.Warnings) != 1 || p.Warnings[0] != "www: remove before add, name stays empty until add succeeds" {
		t.Fatalf("warnings = %v", p.Warnings)
	}
}

func TestCnameConflictWithProtectedOrRaw(t *testing.T) {
	cur := snap(rec("@", zone.NS, "ns1.example.net."))
	p, err := Build(cur, listed(map[zone.Key][]string{key("@", zone.CNAME): {"t.example.net."}}), pol())
	wantPolicy(t, p, err, "cname_conflict")
	cur = zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{{Name: "r", Type: "TXT", Content: "x", Count: 2}}}
	p, err = Build(cur, listed(map[zone.Key][]string{key("r", zone.CNAME): {"t.example.net."}}), pol())
	wantPolicy(t, p, err, "cname_conflict")
	cur = snap(rec("h", zone.HTTPS, "1 . alpn=h2"))
	p, err = Build(cur, listed(map[zone.Key][]string{key("h", zone.CNAME): {"t.example.net."}}), pol())
	wantPolicy(t, p, err, "cname_conflict")
}

func TestCnameToA(t *testing.T) {
	cur := snap(rec("c", zone.CNAME, "t1.example.net."))
	p := mustBuild(t, cur, listed(map[zone.Key][]string{
		key("c", zone.CNAME): {},
		key("c", zone.A):     {"192.0.2.1"},
	}), pol())
	checkOps(t, p.Ops,
		op(1, OpRemove, rec("c", zone.CNAME, "t1.example.net.")),
		op(2, OpAdd, rec("c", zone.A, "192.0.2.1"), 1),
	)
	if len(p.Warnings) != 1 {
		t.Fatalf("warnings = %v", p.Warnings)
	}
}

func TestCnameTargetChange(t *testing.T) {
	cur := snap(rec("c", zone.CNAME, "t1.example.net."), rec("d", zone.A, "192.0.2.1"))
	d, _ := DesiredSet(key("c", zone.CNAME), []string{"t2.example.net."})
	p := mustBuild(t, cur, d, pol())
	checkOps(t, p.Ops,
		op(1, OpRemove, rec("c", zone.CNAME, "t1.example.net.")),
		op(2, OpAdd, rec("c", zone.CNAME, "t2.example.net."), 1),
	)
	if len(p.Warnings) != 1 || !strings.HasPrefix(p.Warnings[0], "c: ") {
		t.Fatalf("warnings = %v", p.Warnings)
	}
}

func TestTwoCnamesRejected(t *testing.T) {
	d, _ := DesiredSet(key("c", zone.CNAME), []string{"t1.example.net.", "t2.example.net."})
	p, err := Build(snap(), d, pol())
	wantPolicy(t, p, err, "cname_conflict")
	cur := snap(rec("c", zone.MX, "10 mx.example.net."))
	p, err = Build(cur, listed(map[zone.Key][]string{key("c", zone.CNAME): {"t.example.net."}, key("c", zone.MX): {"10 mx.example.net."}}), pol())
	wantPolicy(t, p, err, "cname_conflict")
}

func TestTxtBytesPreserved(t *testing.T) {
	v := `v=DKIM1; k=rsa; p=MIGf\"x\" ; t=s  `
	cur := snap(rec("@", zone.TXT, v))
	p := mustBuild(t, cur, exportDesired(t, cur, ScopeManaged), pol())
	if !p.Empty() {
		t.Fatalf("expected noop, got %+v", p.Ops)
	}
	nv := v + "x"
	d, _ := DesiredSet(key("@", zone.TXT), []string{nv})
	p = mustBuild(t, cur, d, pol())
	checkOps(t, p.Ops,
		op(1, OpAdd, rec("@", zone.TXT, nv)),
		op(2, OpRemove, rec("@", zone.TXT, v), 1),
	)
}

func TestDeterministic(t *testing.T) {
	cur := snap(
		rec("b", zone.A, "192.0.2.2"), rec("a", zone.A, "192.0.2.1"),
		rec("c", zone.CNAME, "t.example.net."), rec("a", zone.TXT, "x"),
	)
	d := managed(map[zone.Key][]string{
		key("a", zone.A):   {"192.0.2.9", "192.0.2.8"},
		key("a", zone.TXT): {"y"},
		key("c", zone.A):   {"192.0.2.3"},
		key("z", zone.MX):  {"10 mx.example.net."},
	})
	first := mustBuild(t, cur, d, pol())
	for i := 0; i < 50; i++ {
		p := mustBuild(t, cur, d, pol())
		if !reflect.DeepEqual(p, first) {
			t.Fatalf("non-deterministic:\n%+v\n%+v", p, first)
		}
	}
	checkOps(t, first.Ops,
		op(1, OpAdd, rec("a", zone.A, "192.0.2.8")),
		op(2, OpAdd, rec("a", zone.A, "192.0.2.9")),
		op(3, OpAdd, rec("a", zone.TXT, "y")),
		op(4, OpRemove, rec("a", zone.A, "192.0.2.1"), 1, 2),
		op(5, OpRemove, rec("a", zone.TXT, "x"), 3),
		op(6, OpRemove, rec("b", zone.A, "192.0.2.2")),
		op(7, OpRemove, rec("c", zone.CNAME, "t.example.net.")),
		op(8, OpAdd, rec("c", zone.A, "192.0.2.3"), 7),
		op(9, OpAdd, rec("z", zone.MX, "10 mx.example.net.")),
	)
}

func TestPolicyErrorFormat(t *testing.T) {
	var err error = &PolicyError{Code: "scope_conflict", Detail: "www"}
	if !strings.Contains(err.Error(), "scope_conflict") || policyCode(t, err) != "scope_conflict" {
		t.Fatalf("error = %q", err.Error())
	}
}
