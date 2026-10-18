package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

func testPrepared(t *testing.T) Prepared {
	t.Helper()
	want, err := DesiredSet(zone.Key{Name: "www", Type: zone.A}, []string{"192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	acc := Account{Alias: "main", Login: "user", UserID: "42"}
	p, err := Prepare(acc, "example.test", "100", testSnapshot(), want, Policy{MaxDeletes: 3, Rules: testRules()}, func() string { return "00112233aabbccdd" })
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrepare(t *testing.T) {
	p := testPrepared(t)
	if p.PlanID != "00112233aabbccdd" || p.Endpoint != regruapi.Endpoint || p.Zone != "example.test" || p.ServiceID != "100" {
		t.Fatalf("bad prepared header: %+v", p)
	}
	if p.Policy != (FilePolicy{MaxDeletes: 3}) {
		t.Fatalf("policy %+v", p.Policy)
	}
	if p.BaselineFingerprint != Fingerprint("example.test", "100", testSnapshot()) {
		t.Fatal("baseline fingerprint mismatch")
	}
	if len(p.Plan.Ops) != 2 || p.Plan.Ops[0].Kind != OpAdd || p.Plan.Ops[1].Kind != OpRemove {
		t.Fatalf("ops %+v", p.Plan.Ops)
	}
	_, err := Prepare(p.Account, "example.test", "100", testSnapshot(), Desired{Scope: ScopeListed, RRsets: map[zone.Key][]string{{Name: "@", Type: zone.NS}: {"ns9.example.net."}}}, Policy{Rules: testRules()}, func() string { return "x" })
	var pe *PolicyError
	if !errors.As(err, &pe) {
		t.Fatalf("want policy error, got %v", err)
	}
}

func encode(t *testing.T, p Prepared) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := EncodeFile(&buf, p); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFileRoundTrip(t *testing.T) {
	p := testPrepared(t)
	b := encode(t, p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["apiVersion"] != "regru-cli/plan/v1" {
		t.Fatalf("apiVersion %v", m["apiVersion"])
	}
	if _, ok := m["created"].(string); !ok {
		t.Fatal("created missing")
	}
	if strings.Contains(string(b), "Target") || strings.Contains(string(b), "target") {
		t.Fatalf("target must not be written: %s", b)
	}
	got, err := DecodeFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	want := p
	want.Plan = Plan{Ops: p.Plan.Ops, Warnings: p.Plan.Warnings}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n%+v\n%+v", got, want)
	}
}

func mutate(t *testing.T, b []byte, f func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func opsOf(m map[string]any) []any { return m["ops"].([]any) }

func TestDecodeRejects(t *testing.T) {
	base := encode(t, testPrepared(t))
	cases := []struct {
		name string
		in   []byte
		code string
	}{
		{"extra field", mutate(t, base, func(m map[string]any) { m["extra"] = 1 }), "invalid_plan"},
		{"extra nested field", mutate(t, base, func(m map[string]any) { opsOf(m)[0].(map[string]any)["x"] = 1 }), "invalid_plan"},
		{"two objects", append(append([]byte{}, base...), base...), "invalid_plan"},
		{"trailing garbage", append(append([]byte{}, base...), []byte("x")...), "invalid_plan"},
		{"empty", nil, "invalid_plan"},
		{"api v2", mutate(t, base, func(m map[string]any) { m["apiVersion"] = "regru-cli/plan/v2" }), "stale_plan"},
		{"duplicate seq", mutate(t, base, func(m map[string]any) { opsOf(m)[1].(map[string]any)["seq"] = 1 }), "invalid_plan"},
		{"seq gap", mutate(t, base, func(m map[string]any) {
			opsOf(m)[1].(map[string]any)["seq"] = 3
			opsOf(m)[1].(map[string]any)["depends_on"] = []any{}
		}), "invalid_plan"},
		{"missing dependency", mutate(t, base, func(m map[string]any) { opsOf(m)[1].(map[string]any)["depends_on"] = []any{7} }), "invalid_plan"},
		{"forward dependency", mutate(t, base, func(m map[string]any) {
			opsOf(m)[0].(map[string]any)["depends_on"] = []any{2}
			opsOf(m)[1].(map[string]any)["depends_on"] = []any{}
		}), "invalid_plan"},
		{"self dependency", mutate(t, base, func(m map[string]any) { opsOf(m)[1].(map[string]any)["depends_on"] = []any{2} }), "invalid_plan"},
		{"negative max_deletes", mutate(t, base, func(m map[string]any) { m["policy"].(map[string]any)["max_deletes"] = -1 }), "invalid_plan"},
		{"missing max_deletes", mutate(t, base, func(m map[string]any) { delete(m["policy"].(map[string]any), "max_deletes") }), "invalid_plan"},
		{"invalid A in desired", mutate(t, base, func(m map[string]any) {
			m["desired"].(map[string]any)["rrsets"].([]any)[0].(map[string]any)["values"] = []any{"192.0.2.300"}
		}), "invalid_plan"},
		{"name outside zone in desired", mutate(t, base, func(m map[string]any) {
			m["desired"].(map[string]any)["rrsets"].([]any)[0].(map[string]any)["name"] = "www.example.net."
		}), "invalid_plan"},
		{"bad scope", mutate(t, base, func(m map[string]any) { m["desired"].(map[string]any)["scope"] = "all" }), "invalid_plan"},
		{"unknown op", mutate(t, base, func(m map[string]any) { opsOf(m)[0].(map[string]any)["op"] = "replace" }), "invalid_plan"},
		{"unknown type", mutate(t, base, func(m map[string]any) { opsOf(m)[0].(map[string]any)["type"] = "PTR" }), "invalid_plan"},
		{"missing user_id", mutate(t, base, func(m map[string]any) { delete(m["account"].(map[string]any), "user_id") }), "invalid_plan"},
		{"missing plan_id", mutate(t, base, func(m map[string]any) { delete(m, "plan_id") }), "invalid_plan"},
		{"missing service_id", mutate(t, base, func(m map[string]any) { m["service_id"] = "" }), "invalid_plan"},
		{"bad created", mutate(t, base, func(m map[string]any) { m["created"] = "yesterday" }), "invalid_plan"},
		{"bad fingerprint", mutate(t, base, func(m map[string]any) { m["baseline_fingerprint"] = "fp1:sha256:zz" }), "invalid_plan"},
		{"future fingerprint", mutate(t, base, func(m map[string]any) { m["baseline_fingerprint"] = "fp2:sha512:00" }), "stale_plan"},
		{"not object", []byte(`[]`), "invalid_plan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeFile(bytes.NewReader(c.in))
			if err == nil {
				t.Fatal("expected error")
			}
			if got := codeOf(err); got != c.code {
				t.Fatalf("code %q, want %q (%v)", got, c.code, err)
			}
		})
	}
}

func TestDecodeNormalizesDesired(t *testing.T) {
	base := encode(t, testPrepared(t))
	in := mutate(t, base, func(m map[string]any) {
		rr := m["desired"].(map[string]any)["rrsets"].([]any)[0].(map[string]any)
		rr["name"] = "WWW.example.test."
	})
	p, err := DecodeFile(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Desired.RRsets[zone.Key{Name: "www", Type: zone.A}]; !ok {
		t.Fatalf("desired not normalized: %+v", p.Desired)
	}
}
