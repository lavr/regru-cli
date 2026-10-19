package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func testEnv(vars map[string]string) (Env, *bytes.Buffer, *bytes.Buffer) {
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	return Env{
		Stdout: out,
		Stderr: errb,
		Stdin:  strings.NewReader(""),
		LookupEnv: func(k string) (string, bool) {
			v, ok := vars[k]
			return v, ok
		},
		IsTTY: func() bool { return false },
		Now:   time.Now,
		Home:  "/home/example",
	}, out, errb
}

func decodeOne(t *testing.T, b *bytes.Buffer) Envelope {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b.Bytes()))
	var e Envelope
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("decode: %v; stdout=%q", err, b.String())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("expected single JSON document, got extra: %v", err)
	}
	return e
}

func TestVersionJSON(t *testing.T) {
	env, out, errb := testEnv(nil)
	code := Execute([]string{"version", "--output", "json"}, env, nil)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, out)
	if e.SchemaVersion != 1 || e.Status != "success" {
		t.Fatalf("envelope: %+v", e)
	}
	data, ok := e.Data.(map[string]any)
	if !ok || data["version"] != "dev" {
		t.Fatalf("data: %#v", e.Data)
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %q", errb.String())
	}
	raw := out.String()
	if !strings.Contains(raw, `"errors":[]`) || !strings.Contains(raw, `"warnings":[]`) {
		t.Fatalf("errors/warnings must be arrays: %s", raw)
	}
}

func TestUnknownFlagJSON(t *testing.T) {
	env, out, _ := testEnv(nil)
	code := Execute([]string{"version", "--bogus", "--output", "json"}, env, nil)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, out)
	if e.Status != "failed" || len(e.Errors) == 0 || e.Errors[0].Code != "usage" {
		t.Fatalf("envelope: %+v", e)
	}
}

func TestOutputFromEnv(t *testing.T) {
	env, out, _ := testEnv(map[string]string{"REGRU_OUTPUT": "json"})
	if code := Execute([]string{"version"}, env, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if e := decodeOne(t, out); e.Status != "success" {
		t.Fatalf("envelope: %+v", e)
	}

	env, out, _ = testEnv(map[string]string{"REGRU_OUTPUT": "json"})
	if code := Execute([]string{"version", "--output", "text"}, env, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Fatalf("expected text, got %q", out.String())
	}
}

func TestBadOutputValueTextFallback(t *testing.T) {
	env, out, errb := testEnv(nil)
	code := Execute([]string{"version", "--output", "yaml"}, env, nil)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout: %q", out.String())
	}
	if !strings.Contains(errb.String(), "invalid --output") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestBadOutputValueJSONFallback(t *testing.T) {
	env, out, _ := testEnv(map[string]string{"REGRU_OUTPUT": "json"})
	code := Execute([]string{"version", "--output", "yaml"}, env, nil)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, out)
	if len(e.Errors) == 0 || e.Errors[0].Code != "usage" {
		t.Fatalf("envelope: %+v", e)
	}
}

func TestBuildVersionFromModuleInfo(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc123"},
		{Key: "vcs.time", Value: "2026-10-19T10:00:00Z"},
	}}
	if v, c, d := buildVersion(info, true); v != "v0.1.0" || c != "abc123" || d != "2026-10-19T10:00:00Z" {
		t.Fatalf("%s %s %s", v, c, d)
	}
	if v, _, _ := buildVersion(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true); v != "dev" {
		t.Fatalf("devel: %s", v)
	}
	if v, c, d := buildVersion(nil, false); v != "dev" || c != "none" || d != "unknown" {
		t.Fatalf("no info: %s %s %s", v, c, d)
	}
}
