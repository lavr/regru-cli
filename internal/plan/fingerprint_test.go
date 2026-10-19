package plan

import (
	"strings"
	"testing"

	"github.com/lavr/regru-cli/internal/zone"
)

func TestFingerprintOrderIndependent(t *testing.T) {
	a := testSnapshot()
	b := testSnapshot()
	b.Records[0], b.Records[3] = b.Records[3], b.Records[0]
	b.Records[1], b.Records[2] = b.Records[2], b.Records[1]
	b.Raw[0], b.Raw[1] = b.Raw[1], b.Raw[0]
	fa := Fingerprint("example.test", "100", a)
	fb := Fingerprint("example.test", "100", b)
	if fa != fb {
		t.Fatalf("fingerprint depends on order: %s vs %s", fa, fb)
	}
	if !strings.HasPrefix(fa, "fp1:sha256:") || len(fa) != len("fp1:sha256:")+64 {
		t.Fatalf("bad format %q", fa)
	}
	one, two := 1, 2
	c := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{
		{Name: "x", Type: "HTTPS", Content: "1 .", Prio: &one, Count: 1},
		{Name: "x", Type: "HTTPS", Content: "1 .", Prio: &two, Count: 1},
	}}
	d := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{c.Raw[1], c.Raw[0]}}
	if Fingerprint("example.test", "1", c) != Fingerprint("example.test", "1", d) {
		t.Fatal("raw ties ordering leaks into fingerprint")
	}
}

func TestFingerprintRawNewlineNoCollision(t *testing.T) {
	a := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{
		{Name: "x", Type: "TXT", Content: "a\nW\tb", Count: 1},
	}}
	b := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{
		{Name: "x", Type: "TXT", Content: "a", Count: 1},
		{Name: "W", Type: "b", Content: "", Count: 1},
	}}
	if Fingerprint("example.test", "1", a) == Fingerprint("example.test", "1", b) {
		t.Fatal("collision between one raw with newline and two raws")
	}
	p := 0
	c := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{{Name: "x", Type: "MX", Content: "m", Prio: &p, Count: 1}}}
	d := zone.Snapshot{Zone: "example.test", Raw: []zone.Raw{{Name: "x", Type: "MX", Content: "m", Count: 1}}}
	if Fingerprint("example.test", "1", c) == Fingerprint("example.test", "1", d) {
		t.Fatal("prio 0 and null prio collide")
	}
}

func TestFingerprintIncludesService(t *testing.T) {
	s := testSnapshot()
	if Fingerprint("example.test", "100", s) == Fingerprint("example.test", "101", s) {
		t.Fatal("service id not included")
	}
	if Fingerprint("example.test", "100", s) == Fingerprint("example.net", "100", s) {
		t.Fatal("zone not included")
	}
	s2 := testSnapshot()
	s2.SOA = &zone.SOA{TTL: 1, MinimumTTL: 2}
	if Fingerprint("example.test", "100", s) == Fingerprint("example.test", "100", s2) {
		t.Fatal("soa not included")
	}
}
