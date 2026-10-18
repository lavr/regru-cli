package plan

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lavr/regru-cli/internal/zone"
)

func testRules() Rules {
	rw := zone.Cap{Read: true, Write: true, ExactDelete: true}
	return Rules{
		Caps: map[zone.Type]zone.Cap{
			zone.A: rw, zone.AAAA: rw, zone.CNAME: rw, zone.MX: rw,
			zone.TXT: rw, zone.NS: rw, zone.SRV: rw, zone.CAA: rw,
			zone.HTTPS: {Read: true},
		},
		Protected: func(k zone.Key) bool { return k.Type == zone.NS && k.Name == "@" },
	}
}

func testSnapshot() zone.Snapshot {
	one := 1
	return zone.Snapshot{
		Zone: "example.test",
		Records: []zone.Record{
			{Name: "www", Type: zone.A, Value: "192.0.2.200"},
			{Name: "@", Type: zone.NS, Value: "ns2.reg.ru."},
			{Name: "@", Type: zone.A, Value: "192.0.2.200"},
			{Name: "@", Type: zone.NS, Value: "ns1.reg.ru."},
		},
		Raw: []zone.Raw{
			{Name: "x", Type: "HTTPS", Content: "1 . alpn=h3", Prio: &one, Count: 1},
			{Name: "y", Type: "HTTPS", Content: "0 z.example.net.", Prio: nil, Count: 1},
		},
		SOA: &zone.SOA{TTL: 86400, MinimumTTL: 10800},
	}
}

func codeOf(err error) string {
	var c interface{ Code() string }
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}

func TestExportIsDeterministic(t *testing.T) {
	want, err := os.ReadFile("testdata/export_baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := testSnapshot()
	a, err := ExportManifest("acc", s, ScopeListed, testRules())
	if err != nil {
		t.Fatal(err)
	}
	s2 := testSnapshot()
	s2.Records[0], s2.Records[3] = s2.Records[3], s2.Records[0]
	s2.Raw[0], s2.Raw[1] = s2.Raw[1], s2.Raw[0]
	b, err := ExportManifest("acc", s2, ScopeListed, testRules())
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("exports differ:\n%s\n%s", a, b)
	}
	if string(a) != string(want) {
		t.Fatalf("export differs from golden:\n%s", a)
	}
}

func TestExportParseRoundTrip(t *testing.T) {
	out, err := ExportManifest("acc", testSnapshot(), ScopeManaged, testRules())
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[zone.Key][]string{
		{Name: "@", Type: zone.A}:   {"192.0.2.200"},
		{Name: "www", Type: zone.A}: {"192.0.2.200"},
	}
	if !reflect.DeepEqual(m.Desired.RRsets, want) {
		t.Fatalf("rrsets: %v", m.Desired.RRsets)
	}
	if m.Desired.Scope != ScopeManaged || m.Account != "acc" || m.Zone != "example.test" {
		t.Fatalf("header: %+v", m)
	}
	if len(m.Readonly) != 2 {
		t.Fatalf("readonly: %v", m.Readonly)
	}
}

const head = "apiVersion: regru-cli/v1\nkind: Zone\naccount: acc\nzone: example.test\n"

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name, doc, code string
	}{
		{"no scope", head + "rrsets: []\n", "invalid_manifest"},
		{"no rrsets", head + "scope: listed-rrsets\n", "invalid_manifest"},
		{"rrsets null", head + "scope: listed-rrsets\nrrsets: null\n", "invalid_manifest"},
		{"bool value", head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: TXT, values: [true]}\n", "invalid_value"},
		{"int value", head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: TXT, values: [1]}\n", "invalid_value"},
		{"null value", head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: TXT, values: [null]}\n", "invalid_value"},
		{"dup value", head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: A, values: [192.0.2.1, 192.0.2.1]}\n", "duplicate"},
		{"dup rrset", head + "scope: listed-rrsets\nrrsets:\n  - {name: WWW, type: A, values: [\"192.0.2.1\"]}\n  - {name: www.example.test., type: A, values: [\"192.0.2.2\"]}\n", "duplicate"},
		{"unknown field", head + "scope: listed-rrsets\nrrsets: []\nextra: 1\n", "invalid_manifest"},
		{"unknown nested", head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: A, values: [], ttl: 5}\n", "invalid_manifest"},
		{"bad apiVersion", strings.Replace(head, "v1", "v2", 1) + "scope: listed-rrsets\nrrsets: []\n", "invalid_manifest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(c.doc))
			if err == nil {
				t.Fatal("expected error")
			}
			if got := codeOf(err); got != c.code {
				t.Fatalf("code %q, want %q (%v)", got, c.code, err)
			}
		})
	}
}

func TestParseAcceptsYesAsTXT(t *testing.T) {
	m, err := ParseManifest([]byte(head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: TXT, values: [yes]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := m.Desired.RRsets[zone.Key{Name: "www", Type: zone.TXT}]
	if !reflect.DeepEqual(got, []string{"yes"}) {
		t.Fatalf("got %v", got)
	}
}

func TestValidateReadonlyWritable(t *testing.T) {
	doc := head + "scope: listed-rrsets\nrrsets: []\nreadonly:\n  rrsets:\n    - {name: www, type: A, values: [\"192.0.2.1\"]}\n"
	m, err := ParseManifest([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := codeOf(ValidateManifest(m, testRules())); got != "readonly_writable" {
		t.Fatalf("code %q", got)
	}
	out, err := ExportManifest("acc", testSnapshot(), ScopeListed, testRules())
	if err != nil {
		t.Fatal(err)
	}
	m, err = ParseManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(m, testRules()); err != nil {
		t.Fatal(err)
	}
}

func TestExportWithUnparseableReadonlyRoundTrips(t *testing.T) {
	s := testSnapshot()
	s.Records = append(s.Records, zone.Record{Name: "svc", Type: zone.HTTPS, Value: "1 . alpn=h3"})
	out, err := ExportManifest("acc", s, ScopeListed, testRules())
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(m, testRules()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range m.Readonly {
		if r.Type == zone.HTTPS && r.Name == "svc" && r.Value == "1 . alpn=h3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("readonly: %v", m.Readonly)
	}
}

func TestParseReadonlyNonASCIITXT(t *testing.T) {
	doc := head + "scope: listed-rrsets\nrrsets: []\nreadonly:\n  rrsets:\n    - {name: \"@\", type: TXT, values: [\"café\"]}\n"
	m, err := ParseManifest([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Readonly) != 1 || m.Readonly[0].Value != "café" {
		t.Fatalf("readonly: %v", m.Readonly)
	}
	_, err = ParseManifest([]byte(head + "scope: listed-rrsets\nrrsets:\n  - {name: www, type: TXT, values: [\"café\"]}\n"))
	if codeOf(err) != "invalid_value" {
		t.Fatalf("desired must stay strict: %v", err)
	}
}
