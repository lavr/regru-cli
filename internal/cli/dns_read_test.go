package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lavr/regru-cli/internal/plan"
)

func t12Run(t *testing.T, api *fakeAPI, args ...string) (int, *Deps) {
	t.Helper()
	d := newTestDeps(t, api)
	return Execute(args, d.Env, d), d
}

func TestExportStdoutIsManifest(t *testing.T) {
	code, d := t12Run(t, newFakeAPI(t), "dns", "export", "example.test")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderrOf(d))
	}
	m, err := plan.ParseManifest(stdoutOf(d).Bytes())
	if err != nil {
		t.Fatalf("%v\n%s", err, stdoutOf(d))
	}
	if m.Zone != "example.test" || m.Account != "env" {
		t.Fatalf("%+v", m)
	}
}

func TestExportJSONEnvelope(t *testing.T) {
	code, d := t12Run(t, newFakeAPI(t), "dns", "export", "EXAMPLE.test.", "--output", "json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, stdoutOf(d))
	data := e.Data.(map[string]any)
	s, ok := data["manifest"].(string)
	if !ok {
		t.Fatalf("%+v", data)
	}
	if _, err := plan.ParseManifest([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func TestExportFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "z.yaml")
	code, d := t12Run(t, newFakeAPI(t), "dns", "export", "example.test", "--file", f, "--output", "json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.ParseManifest(b); err != nil {
		t.Fatal(err)
	}
	data := decodeOne(t, stdoutOf(d)).Data.(map[string]any)
	if data["file"] != f || data["rrsets"] == nil {
		t.Fatalf("%+v", data)
	}
}

func TestExportBadScope(t *testing.T) {
	code, _ := t12Run(t, newFakeAPI(t), "dns", "export", "example.test", "--scope", "bogus")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestGetFilterByName(t *testing.T) {
	code, d := t12Run(t, newFakeAPI(t), "dns", "get", "example.test", "--name", "WWW", "--output", "json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var data struct {
		Zone      string `json:"zone"`
		ServiceID string `json:"service_id"`
		Records   []struct{ Name, Type, Value string }
		Raw       []any `json:"raw"`
		SOA       any   `json:"soa"`
	}
	raw, _ := json.Marshal(decodeOne(t, stdoutOf(d)).Data)
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Zone != "example.test" || data.ServiceID != "1000001" || len(data.Records) != 1 || data.Records[0].Name != "www" || data.Records[0].Type != "A" {
		t.Fatalf("%+v", data)
	}
}

func TestGetFilterByType(t *testing.T) {
	code, d := t12Run(t, newFakeAPI(t), "dns", "get", "example.test", "--type", "ns")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := stdoutOf(d).String()
	if !strings.HasPrefix(out, "NAME") || !strings.Contains(out, "ns1.reg.ru") || strings.Contains(out, "192.0.2.200") {
		t.Fatalf("%q", out)
	}
}

func TestGetBadType(t *testing.T) {
	code, _ := t12Run(t, newFakeAPI(t), "dns", "get", "example.test", "--type", "BOGUS")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestDomainNotOnRegruNS(t *testing.T) {
	api := newFakeAPI(t)
	api.setNSS("example.test", "ns1.example.net")
	code, d := t12Run(t, api, "dns", "get", "example.test", "--output", "json")
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].ProviderCode != "DOMAIN_IS_NOT_USE_REGRU_NSS" {
		t.Fatalf("%+v", e)
	}
}

func TestIncompleteReadIsError(t *testing.T) {
	api := newFakeAPI(t)
	api.set(func(f *fakeAPI) { f.failNextRead = true })
	code, d := t12Run(t, api, "dns", "get", "example.test", "--output", "json")
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].Code != "incomplete_read" || e.Data != nil {
		t.Fatalf("%+v", e)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestExportStdoutWriteFailureIsError(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	env := d.Env
	env.Stdout = failingWriter{}
	if code := Execute([]string{"dns", "export", "example.test"}, env, d); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderrOf(d).String(), "broken pipe") {
		t.Fatalf("stderr %q", stderrOf(d).String())
	}
}
