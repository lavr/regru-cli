package regruzone

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

func fixtureResponse(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "regruapi", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return string(f.Response)
}

func fixtureRecords(t *testing.T, name string) regruapi.ZoneRecords {
	t.Helper()
	var env struct {
		Answer struct {
			Domains []struct {
				Dname     string        `json:"dname"`
				ServiceID string        `json:"service_id"`
				RRs       []regruapi.RR `json:"rrs"`
				S         *struct {
					TTL        string `json:"ttl"`
					MinimumTTL string `json:"minimum_ttl"`
				} `json:"soa"`
			} `json:"domains"`
		} `json:"answer"`
	}
	if err := json.Unmarshal([]byte(fixtureResponse(t, name)), &env); err != nil {
		t.Fatal(err)
	}
	d := env.Answer.Domains[0]
	z := regruapi.ZoneRecords{Domain: d.Dname, ServiceID: d.ServiceID, RRs: d.RRs}
	if d.S != nil {
		z.SOA = &regruapi.SOAStrings{TTL: d.S.TTL, MinimumTTL: d.S.MinimumTTL}
	}
	return z
}

func rr(sub, typ, content string, prio *int) regruapi.RR {
	r := regruapi.RR{Subname: sub, Rectype: typ, Content: content, State: "A"}
	if prio != nil {
		r.Prio = regruapi.FlexInt{Value: *prio, Valid: true}
	}
	return r
}

func ip(v int) *int { return &v }

func synthetic(rrs ...regruapi.RR) regruapi.ZoneRecords {
	return regruapi.ZoneRecords{Domain: "example.test", ServiceID: "1000001", RRs: rrs}
}

func mustFromAPI(t *testing.T, z regruapi.ZoneRecords) Observed {
	t.Helper()
	o, err := FromAPI("example.test", z)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func hasRecord(s zone.Snapshot, r zone.Record) bool {
	for _, x := range s.Records {
		if x == r {
			return true
		}
	}
	return false
}

func TestFromAPICAA(t *testing.T) {
	o := mustFromAPI(t, fixtureRecords(t, "zone_get_resource_records_caa_pair"))
	issue := zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue a.example.net"}
	wild := zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issuewild a.example.net"}
	for _, r := range []zone.Record{issue, wild} {
		if !hasRecord(o.Snapshot, r) {
			t.Fatalf("missing %v in %v", r, o.Snapshot.Records)
		}
	}
	if s := o.Selectors[issue]; s != (Selector{Subdomain: "caa1.probe", Priority: "0", Content: "0 issue \"a.example.net\""}) {
		t.Fatalf("issue selector %#v", s)
	}
	if s := o.Selectors[wild]; s != (Selector{Subdomain: "caa1.probe", Priority: "0", Content: "0 issuewild \"a.example.net\""}) {
		t.Fatalf("issuewild selector %#v", s)
	}
}

func TestFromAPICAAEscaped(t *testing.T) {
	content := `0 issue "a\"b.example.net"`
	o := mustFromAPI(t, synthetic(rr("c", "CAA", content, ip(0))))
	r := zone.Record{Name: "c", Type: zone.CAA, Value: `0 issue a"b.example.net`}
	if !hasRecord(o.Snapshot, r) {
		t.Fatalf("records %v raw %v", o.Snapshot.Records, o.Snapshot.Raw)
	}
	if s := o.Selectors[r]; s != (Selector{Subdomain: "c", Priority: "0", Content: content}) {
		t.Fatalf("selector %#v", s)
	}
}

func TestFromAPICAAMalformedIsRaw(t *testing.T) {
	for _, c := range []string{`0 issue a.example.net`, `0 issue "a"b"`, `0 issue "unterminated`, `0 ISSUE "a.example.net"`, `0 issue "a\"`, `0 issue ""`, `0 issue "\"q\""`} {
		o := mustFromAPI(t, synthetic(rr("c", "CAA", c, ip(0))))
		if len(o.Snapshot.Records) != 0 || len(o.Snapshot.Raw) != 1 || len(o.Selectors) != 0 {
			t.Errorf("%q: records %v raw %v", c, o.Snapshot.Records, o.Snapshot.Raw)
		}
	}
}

func TestFromAPISRV(t *testing.T) {
	o := mustFromAPI(t, fixtureRecords(t, "zone_get_resource_records_srv_pair"))
	r := zone.Record{Name: "_s1._tcp.probe", Type: zone.SRV, Value: "10 5 5060 sip.example.net."}
	if !hasRecord(o.Snapshot, r) {
		t.Fatalf("records %v", o.Snapshot.Records)
	}
	if s := o.Selectors[r]; s != (Selector{Subdomain: "_s1._tcp.probe", Priority: "10", Content: "5 5060 sip.example.net."}) {
		t.Fatalf("selector %#v", s)
	}
}

func TestFromAPISRVFieldMismatchIsRaw(t *testing.T) {
	x := rr("_s._tcp", "SRV", "5 5060 sip.example.net.", ip(10))
	x.Weight = &regruapi.FlexInt{Value: 6, Valid: true}
	o := mustFromAPI(t, synthetic(x))
	if len(o.Snapshot.Records) != 0 || len(o.Snapshot.Raw) != 1 {
		t.Fatalf("records %v raw %v", o.Snapshot.Records, o.Snapshot.Raw)
	}
}

func TestFromAPIMXWithoutPrioIsRaw(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("m", "MX", "mx.example.net.", nil), rr("s", "SRV", "5 5060 sip.example.net.", nil)))
	if len(o.Snapshot.Records) != 0 || len(o.Snapshot.Raw) != 2 || len(o.Selectors) != 0 {
		t.Fatalf("records %v raw %v", o.Snapshot.Records, o.Snapshot.Raw)
	}
}

func TestFromAPINonCanonical(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("m", "MX", "MX-A.Example.NET.", ip(10)), rr("v6", "AAAA", "2001:DB8:0::1", nil)))
	mx := zone.Record{Name: "m", Type: zone.MX, Value: "10 mx-a.example.net."}
	aaaa := zone.Record{Name: "v6", Type: zone.AAAA, Value: "2001:db8::1"}
	if s := o.Selectors[mx]; s != (Selector{Subdomain: "m", Priority: "10", Content: "MX-A.Example.NET."}) {
		t.Fatalf("mx selector %#v records %v", s, o.Snapshot.Records)
	}
	if s := o.Selectors[aaaa]; s != (Selector{Subdomain: "v6", Priority: "0", Content: "2001:DB8:0::1"}) {
		t.Fatalf("aaaa selector %#v records %v", s, o.Snapshot.Records)
	}
	if !hasRecord(o.Snapshot, mx) || !hasRecord(o.Snapshot, aaaa) {
		t.Fatalf("records %v", o.Snapshot.Records)
	}
}

func TestFromAPIAmbiguous(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("d", "A", "192.0.2.1", ip(0)), rr("d", "A", "192.0.2.1", ip(0))))
	if len(o.Snapshot.Records) != 0 || len(o.Selectors) != 0 {
		t.Fatalf("records %v selectors %v", o.Snapshot.Records, o.Selectors)
	}
	if len(o.Snapshot.Raw) != 1 || o.Snapshot.Raw[0].Count != 2 || o.Snapshot.Raw[0].Content != "192.0.2.1" || o.Snapshot.Raw[0].Name != "d" || o.Snapshot.Raw[0].Type != "A" {
		t.Fatalf("raw %#v", o.Snapshot.Raw)
	}
}

func TestFromAPIAmbiguousDifferentContent(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("m", "MX", "MX.example.net.", ip(10)), rr("m", "MX", "mx.example.net.", ip(10)), rr("m", "MX", "other.example.net.", ip(10))))
	if len(o.Snapshot.Records) != 1 || len(o.Selectors) != 1 {
		t.Fatalf("records %v", o.Snapshot.Records)
	}
	if o.Selectors[zone.Record{Name: "m", Type: zone.MX, Value: "10 other.example.net."}] != (Selector{Subdomain: "m", Priority: "10", Content: "other.example.net."}) {
		t.Fatalf("selectors %v", o.Selectors)
	}
	if len(o.Snapshot.Raw) != 2 {
		t.Fatalf("raw %#v", o.Snapshot.Raw)
	}
	for _, r := range o.Snapshot.Raw {
		if r.Count != 1 || r.Prio == nil || *r.Prio != 10 {
			t.Fatalf("raw %#v", r)
		}
	}
}

func TestFromAPIBaseline(t *testing.T) {
	o := mustFromAPI(t, fixtureRecords(t, "zone_get_resource_records_baseline"))
	want := []zone.Record{
		{Name: "@", Type: zone.A, Value: "192.0.2.200"},
		{Name: "www", Type: zone.A, Value: "192.0.2.200"},
		{Name: "@", Type: zone.NS, Value: "ns1.reg.ru."},
		{Name: "@", Type: zone.NS, Value: "ns2.reg.ru."},
	}
	if len(o.Snapshot.Records) != len(want) {
		t.Fatalf("records %v", o.Snapshot.Records)
	}
	for _, r := range want {
		if !hasRecord(o.Snapshot, r) {
			t.Fatalf("missing %v", r)
		}
	}
	if len(o.Snapshot.Raw) != 0 {
		t.Fatalf("raw %v", o.Snapshot.Raw)
	}
	if o.Snapshot.SOA == nil || *o.Snapshot.SOA != (zone.SOA{TTL: 86400, MinimumTTL: 10800}) {
		t.Fatalf("soa %v", o.Snapshot.SOA)
	}
	if o.ServiceID != "1000001" || o.Snapshot.Zone != "example.test" {
		t.Fatalf("service %q zone %q", o.ServiceID, o.Snapshot.Zone)
	}
}

func TestFromAPIBadSOA(t *testing.T) {
	z := fixtureRecords(t, "zone_get_resource_records_baseline")
	z.SOA = &regruapi.SOAStrings{TTL: "1y", MinimumTTL: "3h"}
	if _, err := FromAPI("example.test", z); err == nil {
		t.Fatal("expected error")
	}
	z.SOA = &regruapi.SOAStrings{TTL: "1d", MinimumTTL: ""}
	if _, err := FromAPI("example.test", z); err == nil {
		t.Fatal("expected error")
	}
}

func TestFromAPINoSOA(t *testing.T) {
	o := mustFromAPI(t, synthetic())
	if o.Snapshot.SOA != nil || len(o.Snapshot.Records) != 0 {
		t.Fatalf("%#v", o.Snapshot)
	}
}

func TestFromAPIHTTPS(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("x", "HTTPS", "1 . alpn=h3", ip(1)), rr("y", "TYPE65534", "abc", nil)))
	if len(o.Snapshot.Records) != 0 || len(o.Selectors) != 0 {
		t.Fatalf("records %v", o.Snapshot.Records)
	}
	if len(o.Snapshot.Raw) != 2 {
		t.Fatalf("raw %#v", o.Snapshot.Raw)
	}
	for _, r := range o.Snapshot.Raw {
		if r.Name == "x" && (r.Type != "HTTPS" || r.Content != "1 . alpn=h3" || r.Prio == nil || *r.Prio != 1 || r.Count != 1) {
			t.Fatalf("raw %#v", r)
		}
		if r.Name == "y" && r.Prio != nil {
			t.Fatalf("raw %#v", r)
		}
	}
}

func TestFromAPINonCanonicalSubnameKeepsObservedSubdomain(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("WWW", "A", "192.0.2.5", ip(0))))
	r := zone.Record{Name: "www", Type: zone.A, Value: "192.0.2.5"}
	if !hasRecord(o.Snapshot, r) {
		t.Fatalf("records %v", o.Snapshot.Records)
	}
	s, ok := o.Selectors[r]
	if !ok || s != (Selector{Subdomain: "WWW", Priority: "0", Content: "192.0.2.5"}) {
		t.Fatalf("selector %#v %v", s, ok)
	}
	fn, params := RemoveCall(r, s)
	if fn != "remove_record" || params["subdomain"] != "WWW" || params["record_type"] != "A" || params["priority"] != "0" || params["content"] != "192.0.2.5" {
		t.Fatalf("%s %v", fn, params)
	}
}

func TestFromAPIBadSubnameIsRaw(t *testing.T) {
	o := mustFromAPI(t, synthetic(rr("a..b", "A", "192.0.2.5", ip(0)), rr("x.example.net.", "A", "192.0.2.6", ip(0))))
	if len(o.Snapshot.Records) != 0 || len(o.Snapshot.Raw) != 2 {
		t.Fatalf("records %v raw %v", o.Snapshot.Records, o.Snapshot.Raw)
	}
}

func TestFromAPIBadZone(t *testing.T) {
	if _, err := FromAPI("", synthetic()); err == nil {
		t.Fatal("expected error")
	}
}

func TestSelectorsMatchProbes(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "regruapi", "testdata", "zone_get_resource_records_*_pair.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		name = name[:len(name)-len(".json")]
		z := fixtureRecords(t, name)
		o := mustFromAPI(t, z)
		if len(o.Snapshot.Raw) != 0 {
			t.Fatalf("%s raw %v", name, o.Snapshot.Raw)
		}
		if len(o.Selectors) != len(z.RRs) || len(o.Snapshot.Records) != len(z.RRs) {
			t.Fatalf("%s: %d selectors, %d records, %d rrs", name, len(o.Selectors), len(o.Snapshot.Records), len(z.RRs))
		}
		for _, x := range z.RRs {
			want := Selector{Subdomain: x.Subname, Priority: "0", Content: x.Content}
			if x.Prio.Valid {
				want.Priority = itoa(x.Prio.Value)
			}
			found := false
			for rec, s := range o.Selectors {
				if rec.Name == x.Subname && string(rec.Type) == x.Rectype && s == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: no selector %v for %v", name, want, x)
			}
			if x.Rectype == "AAAA" && want.Priority != "0" {
				t.Fatalf("%s: aaaa priority %q", name, want.Priority)
			}
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestIsProtected(t *testing.T) {
	if !IsProtected(zone.Key{Name: "@", Type: zone.NS}) {
		t.Fatal("apex NS must be protected")
	}
	for _, k := range []zone.Key{{Name: "sub", Type: zone.NS}, {Name: "@", Type: zone.A}} {
		if IsProtected(k) {
			t.Fatalf("%v is not protected", k)
		}
	}
}

func TestCapabilities(t *testing.T) {
	c := Capabilities()
	full := zone.Cap{Read: true, Write: true, ExactDelete: true}
	for _, ty := range []zone.Type{zone.A, zone.AAAA, zone.CNAME, zone.MX, zone.TXT, zone.NS, zone.SRV, zone.CAA} {
		if c[ty] != full {
			t.Fatalf("%s: %v", ty, c[ty])
		}
	}
	if c[zone.HTTPS] != (zone.Cap{Read: true}) {
		t.Fatalf("HTTPS %v", c[zone.HTTPS])
	}
	if len(c) != 9 {
		t.Fatalf("caps %v", c)
	}
}

func TestFromAPIRawCanonicalNameAndType(t *testing.T) {
	o := mustFromAPI(t, synthetic(
		rr("Dup.Probe", "A", "192.0.2.1", ip(0)),
		rr("dup.probe", "A", "192.0.2.1", ip(0)),
		rr("X", "https", "1 . alpn=h3", ip(1)),
		rr("www.Example.Test.", "A", "192.0.2.7", ip(0)),
		rr("A..B", "A", "192.0.2.8", ip(0)),
	))
	if len(o.Snapshot.Records) != 0 || len(o.Selectors) != 0 {
		t.Fatalf("records %v selectors %v", o.Snapshot.Records, o.Snapshot.Raw)
	}
	got := map[zone.Raw]bool{}
	for _, r := range o.Snapshot.Raw {
		r.Prio = nil
		got[r] = true
	}
	want := []zone.Raw{
		{Name: "dup.probe", Type: "A", Content: "192.0.2.1", Count: 2},
		{Name: "x", Type: "HTTPS", Content: "1 . alpn=h3", Count: 1},
		{Name: "www", Type: "A", Content: "192.0.2.7", Count: 1},
		{Name: "a..b", Type: "A", Content: "192.0.2.8", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("raw %#v", o.Snapshot.Raw)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("missing %#v in %#v", w, o.Snapshot.Raw)
		}
	}
}
