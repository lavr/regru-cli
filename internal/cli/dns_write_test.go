package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lavr/regru-cli/internal/apply"
	"github.com/lavr/regru-cli/internal/plan"
)

const t13Manifest = `apiVersion: regru-cli/v1
kind: Zone
account: env
zone: example.test
scope: listed-rrsets
rrsets:
  - name: www
    type: A
    values: ["192.0.2.1"]
`

const t13NoopManifest = `apiVersion: regru-cli/v1
kind: Zone
account: env
zone: example.test
scope: listed-rrsets
rrsets:
  - name: www
    type: A
    values: ["192.0.2.200"]
`

type t13Opts struct {
	stdin string
	tty   bool
}

func t13Exec(t *testing.T, d *Deps, o t13Opts, args ...string) (int, Envelope) {
	t.Helper()
	stdoutOf(d).Reset()
	stderrOf(d).Reset()
	env := d.Env
	env.Stdin = strings.NewReader(o.stdin)
	tty := o.tty
	env.IsTTY = func() bool { return tty }
	code := Execute(append([]string{"--output", "json"}, args...), env, d)
	return code, decodeOne(t, stdoutOf(d))
}

func t13Run(t *testing.T, d *Deps, args ...string) (int, Envelope) {
	t.Helper()
	return t13Exec(t, d, t13Opts{}, args...)
}

func t13File(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func t13Data(t *testing.T, e Envelope) map[string]any {
	t.Helper()
	m, ok := e.Data.(map[string]any)
	if !ok {
		t.Fatalf("data %T %+v", e.Data, e)
	}
	return m
}

func t13PlanOps(t *testing.T, e Envelope) []any {
	t.Helper()
	p, ok := t13Data(t, e)["plan"].(map[string]any)
	if !ok {
		t.Fatalf("no data.plan: %+v", e)
	}
	ops, ok := p["ops"].([]any)
	if !ok {
		t.Fatalf("no data.plan.ops: %+v", p)
	}
	return ops
}

func t13HasError(e Envelope, code string) bool {
	for _, it := range e.Errors {
		if it.Code == code {
			return true
		}
	}
	return false
}

func t13WWW(api *fakeAPI) []string {
	var out []string
	for _, r := range api.records("example.test") {
		if r.Subname == "www" && r.Rectype == "A" {
			out = append(out, r.Content)
		}
	}
	return out
}

func t13Has(api *fakeAPI, sub, typ, content string) bool {
	for _, r := range api.records("example.test") {
		if r.Subname == sub && r.Rectype == typ && r.Content == content {
			return true
		}
	}
	return false
}

func t13JournalLines(t *testing.T, d *Deps) []map[string]any {
	t.Helper()
	dir := filepath.Join(d.Paths.State, "journal", "1000", "example.test")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal dir: %v %v", entries, err)
	}
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func t13Save(t *testing.T, d *Deps, manifest string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plan.json")
	code, e := t13Run(t, d, "dns", "plan", "--file", t13File(t, "zone.yaml", manifest), "--save", p)
	if code != 0 {
		t.Fatalf("plan --save: %d %+v", code, e)
	}
	return p
}

func TestSetNonTTYNeedsYes(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "set", "example.test", "www", "A", "192.0.2.1")
	if code != 1 || e.Status != StatusConfirmationRequired {
		t.Fatalf("exit %d %+v", code, e)
	}
	if ops := t13PlanOps(t, e); len(ops) != 2 {
		t.Fatalf("ops %v", ops)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestSetWithYes(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "set", "example.test", "www", "A", "192.0.2.1", "--yes")
	if code != 0 || e.Status != StatusSuccess {
		t.Fatalf("exit %d %+v", code, e)
	}
	if got := t13WWW(api); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("www %v", got)
	}
	ex, ok := t13Data(t, e)["execution"].(map[string]any)
	if !ok || ex["status"] != "success" {
		t.Fatalf("execution %+v", e.Data)
	}
	lines := t13JournalLines(t, d)
	if len(lines) == 0 || lines[0]["event"] != "begin" {
		t.Fatalf("journal %v", lines)
	}
	if ops, _ := lines[0]["ops"].([]any); len(ops) != 2 {
		t.Fatalf("begin ops %v", lines[0])
	}
	if last := lines[len(lines)-1]; last["event"] != "finish" || last["status"] != "success" {
		t.Fatalf("finish %v", last)
	}
}

func TestConfirmInteractive(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Exec(t, d, t13Opts{stdin: "n\n", tty: true}, "dns", "add", "example.test", "new", "A", "192.0.2.7")
	if code != 1 || e.Status == StatusSuccess || api.Mutations.Load() != 0 {
		t.Fatalf("declined: exit %d %+v", code, e)
	}
	if !strings.Contains(stderrOf(d).String(), "Apply 1 operations to example.test? [y/N]") {
		t.Fatalf("stderr %q", stderrOf(d).String())
	}
	code, e = t13Exec(t, d, t13Opts{stdin: "y\n", tty: true}, "dns", "add", "example.test", "new", "A", "192.0.2.7")
	if code != 0 || !t13Has(api, "new", "A", "192.0.2.7") {
		t.Fatalf("accepted: exit %d %+v", code, e)
	}
}

func TestDriftDuringConfirmation(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	api.set(func(f *fakeAPI) {
		f.onRead = func() {
			n := 0
			for _, c := range api.recorded() {
				if c.Method == "zone/get_resource_records" {
					n++
				}
			}
			if n == 2 {
				api.set(func(f *fakeAPI) {
					z := f.zones["example.test"]
					z.RRs = append(z.RRs, fakeRR{Subname: "other", Rectype: "A", Content: "192.0.2.50", Prio: intp(0)})
				})
			}
		}
	})
	code, e := t13Exec(t, d, t13Opts{stdin: "y\n", tty: true}, "dns", "set", "example.test", "www", "A", "192.0.2.1")
	if code != 4 || !t13HasError(e, "drift") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestPlanFileDriftBeforeApply(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	api.set(func(f *fakeAPI) {
		z := f.zones["example.test"]
		z.RRs = append(z.RRs, fakeRR{Subname: "other", Rectype: "A", Content: "192.0.2.50", Prio: intp(0)})
	})
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--yes")
	if code != 4 || !t13HasError(e, "drift") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestPlanFileOpsNotReplaced(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	ops := doc["ops"].([]any)
	replaced := false
	for _, o := range ops {
		m := o.(map[string]any)
		if m["op"] == "add" && m["value"] == "192.0.2.1" {
			m["value"] = "192.0.2.2"
			replaced = true
		}
	}
	if !replaced {
		t.Fatalf("ops %v", ops)
	}
	nb, _ := json.Marshal(doc)
	if err := os.WriteFile(p, nb, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.DecodeFile(strings.NewReader(string(nb))); err != nil {
		t.Fatalf("tampered plan must stay valid: %v", err)
	}
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--yes")
	if code != 4 || !t13HasError(e, "plan_mismatch") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestPlanSaveApplyOK(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("plan file %v %v", st, err)
	}
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--yes")
	if code != 0 || e.Status != StatusSuccess {
		t.Fatalf("exit %d %+v", code, e)
	}
	if got := t13WWW(api); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("www %v", got)
	}
}

func TestApplyFileWithYes(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "apply", "--file", t13File(t, "z.yaml", t13Manifest), "--yes")
	if code != 0 || e.Status != StatusSuccess {
		t.Fatalf("exit %d %+v", code, e)
	}
	if got := t13WWW(api); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("www %v", got)
	}
}

func TestApplyDryRun(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "apply", "--file", t13File(t, "z.yaml", t13Manifest), "--dry-run")
	if code != 0 || len(t13PlanOps(t, e)) != 2 || api.Mutations.Load() != 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
}

func TestTxtLeadingDash(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "add", "example.test", "txt", "TXT", "--yes", "--", "-x")
	if code != 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
	if !t13Has(api, "txt", "TXT", "-x") {
		t.Fatalf("records %v", api.records("example.test"))
	}
}

func TestTxtEmpty(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "add", "example.test", "txt", "TXT", "", "--yes")
	if code != 2 || !t13HasError(e, "invalid_value") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("requests %d", n)
	}
}

func TestNameNormalization(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "add", "Example.TEST.", "WWW.example.test.", "A", "192.0.2.5", "--yes")
	if code != 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
	if !t13Has(api, "www", "A", "192.0.2.5") {
		t.Fatalf("records %v", api.records("example.test"))
	}
	before := api.Requests.Load()
	code, e = t13Run(t, d, "dns", "add", "example.test", "www.other.test.", "A", "192.0.2.6", "--yes")
	if code != 2 || !t13HasError(e, "name_outside_zone") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if api.Requests.Load() != before {
		t.Fatal("network used for invalid name")
	}
}

func TestPlanDetailedExitcode(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "plan", "--file", t13File(t, "a.yaml", t13Manifest), "--detailed-exitcode")
	if code != 2 || len(t13PlanOps(t, e)) != 2 {
		t.Fatalf("changes: exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "plan", "--file", t13File(t, "b.yaml", t13NoopManifest), "--detailed-exitcode")
	if code != 0 || len(t13PlanOps(t, e)) != 0 {
		t.Fatalf("no changes: exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "plan", "--file", t13File(t, "c.yaml", "apiVersion: [\n"), "--detailed-exitcode")
	if code != 1 || e.Status != StatusFailed {
		t.Fatalf("invalid: exit %d %+v", code, e)
	}
	code, _ = t13Run(t, d, "dns", "plan", "--file", t13File(t, "d.yaml", "apiVersion: [\n"))
	if code != 2 {
		t.Fatalf("invalid without flag: exit %d", code)
	}
	code, _ = t13Run(t, d, "dns", "plan", "--file", t13File(t, "e.yaml", t13Manifest))
	if code != 0 {
		t.Fatalf("changes without flag: exit %d", code)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestPlanFromStdin(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Exec(t, d, t13Opts{stdin: t13Manifest}, "dns", "plan", "--file", "-")
	if code != 0 || len(t13PlanOps(t, e)) != 2 {
		t.Fatalf("exit %d %+v", code, e)
	}
}

func TestApplyPlanAndFileExclusive(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	f := t13File(t, "z.yaml", t13Manifest)
	code, e := t13Run(t, d, "dns", "apply", "--plan", f, "--file", f, "--yes")
	if code != 2 || !t13HasError(e, "usage") || !strings.Contains(e.Errors[0].Message, "exactly one of") {
		t.Fatalf("both: exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "apply", "--yes")
	if code != 2 || !strings.Contains(e.Errors[0].Message, "exactly one of") {
		t.Fatalf("neither: exit %d %+v", code, e)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("requests %d", n)
	}
}

func TestApplyPlanCannotLoosen(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	before := api.Requests.Load()
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--yes", "--allow-empty-zone")
	if code != 2 || !t13HasError(e, "policy_change_requires_new_plan") {
		t.Fatalf("allow-empty-zone: exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "apply", "--plan", p, "--yes", "--max-deletes", "11")
	if code != 2 || !t13HasError(e, "policy_change_requires_new_plan") {
		t.Fatalf("looser max-deletes: exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "apply", "--plan", p, "--yes", "--max-deletes", "0")
	if code != 1 || !t13HasError(e, "max_deletes_exceeded") {
		t.Fatalf("tighter max-deletes: exit %d %+v", code, e)
	}
	if api.Requests.Load() != before || api.Mutations.Load() != 0 {
		t.Fatalf("requests %d mutations %d", api.Requests.Load()-before, api.Mutations.Load())
	}
	code, e = t13Run(t, d, "dns", "apply", "--plan", p, "--yes", "--max-deletes", "1")
	if code != 0 {
		t.Fatalf("tighter but fits: exit %d %+v", code, e)
	}
}

func TestStdinNeedsYes(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Exec(t, d, t13Opts{stdin: t13Manifest, tty: true}, "dns", "apply", "--file", "-")
	if code != 1 || e.Status != StatusConfirmationRequired {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
	code, e = t13Exec(t, d, t13Opts{stdin: t13Manifest, tty: true}, "dns", "apply", "--file", "-", "--yes")
	if code != 0 {
		t.Fatalf("with --yes: exit %d %+v", code, e)
	}
}

func TestUnknownExitCode(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	api.set(func(f *fakeAPI) {
		f.dropOnMutate = true
		f.onRead = func() {
			if api.Mutations.Load() > 0 {
				api.set(func(f *fakeAPI) { f.failNextRead = true })
			}
		}
	})
	code, e := t13Run(t, d, "dns", "set", "example.test", "www", "A", "192.0.2.1", "--yes")
	if code != 3 || e.Status != StatusUnknown {
		t.Fatalf("exit %d %+v", code, e)
	}
}

func TestFailedExecution(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	api.set(func(f *fakeAPI) { f.failNextMutate = "DOMAIN_BAD_STATE" })
	code, e := t13Run(t, d, "dns", "add", "example.test", "new", "A", "192.0.2.7", "--yes")
	if code != 1 || e.Status != StatusFailed || len(e.Errors) == 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
}

type t13Journal struct {
	begun     bool
	resultErr error
	finishErr error
}

func (j *t13Journal) Begin(plan.Prepared, string) error { j.begun = true; return nil }

func (j *t13Journal) Intent(plan.Op) error { return nil }

func (j *t13Journal) Result(plan.Op, string, string) error { return j.resultErr }

func (j *t13Journal) Finish(string, any) error { return j.finishErr }

func TestJournalFailureNotSuccess(t *testing.T) {
	cases := []struct {
		name string
		j    *t13Journal
	}{
		{"finish", &t13Journal{finishErr: errors.New("disk full")}},
		{"result", &t13Journal{resultErr: errors.New("disk full")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI(t)
			d := newTestDeps(t, api)
			d.OpenJournal = func(string, string, string, string) (apply.Journal, error) { return c.j, nil }
			code, e := t13Run(t, d, "dns", "set", "example.test", "www", "A", "192.0.2.1", "--yes")
			if code != 3 || e.Status != StatusPartial || !t13HasError(e, "journal_write_failed") {
				t.Fatalf("exit %d %+v", code, e)
			}
			if !c.j.begun || !t13Has(api, "www", "A", "192.0.2.1") {
				t.Fatalf("zone not changed: %v", api.records("example.test"))
			}
		})
	}
}

func TestJournalOpenFailure(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	d.OpenJournal = func(string, string, string, string) (apply.Journal, error) { return nil, errors.New("read-only") }
	code, e := t13Run(t, d, "dns", "add", "example.test", "new", "A", "192.0.2.7", "--yes")
	if code != 1 || api.Mutations.Load() != 0 || len(e.Errors) == 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
}

func TestSecondProcessLocked(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	unlock, err := apply.LockZone(d.Paths.State, "1000", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	code, e := t13Run(t, d, "dns", "set", "example.test", "www", "A", "192.0.2.1", "--yes")
	if code != 1 || !t13HasError(e, "zone_locked") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestRmAllValuesAndEmptyZone(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "rm", "example.test", "www", "A", "--all-values", "--yes")
	if code != 0 || len(t13WWW(api)) != 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "rm", "example.test", "@", "A", "192.0.2.200", "--yes")
	if code != 1 || !t13HasError(e, "empty_zone_not_allowed") {
		t.Fatalf("last record: exit %d %+v", code, e)
	}
	code, e = t13Run(t, d, "dns", "rm", "example.test", "@", "A", "192.0.2.200", "--yes", "--allow-empty-zone")
	if code != 0 || t13Has(api, "@", "A", "192.0.2.200") {
		t.Fatalf("allowed: exit %d %+v", code, e)
	}
}

func TestRmArgs(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	if code, e := t13Run(t, d, "dns", "rm", "example.test", "www", "A"); code != 2 {
		t.Fatalf("no values: exit %d %+v", code, e)
	}
	if code, e := t13Run(t, d, "dns", "rm", "example.test", "www", "A", "192.0.2.200", "--all-values"); code != 2 {
		t.Fatalf("both: exit %d %+v", code, e)
	}
	if code, e := t13Run(t, d, "dns", "add", "example.test", "www", "HTTPS", "1 . alpn=h2"); code != 2 {
		t.Fatalf("https: exit %d %+v", code, e)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("requests %d", n)
	}
}

func TestNoopNeedsNoConfirmation(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	code, e := t13Run(t, d, "dns", "add", "example.test", "www", "A", "192.0.2.200")
	if code != 0 || e.Status != StatusSuccess || len(t13PlanOps(t, e)) != 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
}

func TestMutationHelpMentionsAllowEmptyZone(t *testing.T) {
	d := newTestDeps(t, nil)
	root := NewRoot(d.Env, d)
	for _, name := range []string{"rm", "set", "apply"} {
		c, _, err := root.Find([]string{"dns", name})
		if err != nil || c.Name() != name {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(c.Long+c.UsageString(), "--allow-empty-zone") {
			t.Fatalf("%s help lacks --allow-empty-zone", name)
		}
	}
}

func TestToCodedFlowErrors(t *testing.T) {
	cases := []struct {
		err  error
		exit int
		code string
	}{
		{&apply.StaleError{Code: "drift", Detail: "x"}, 4, "drift"},
		{&plan.FileError{CodeValue: "invalid_plan", Reason: "r"}, 4, "invalid_plan"},
		{&plan.FileError{CodeValue: "stale_plan", Reason: "r"}, 4, "stale_plan"},
		{&apply.LockedError{Zone: "example.test", Path: "p"}, 1, "zone_locked"},
	}
	for _, c := range cases {
		got := ToCoded(c.err)
		if got.Exit != c.exit || got.Item.Code != c.code {
			t.Fatalf("%v: %+v", c.err, got)
		}
	}
}

func TestTextOutput(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	env := d.Env
	env.Stdin = strings.NewReader("y\n")
	env.IsTTY = func() bool { return true }
	code := Execute([]string{"dns", "add", "example.test", "new", "A", "192.0.2.7"}, env, d)
	out, errOut := stdoutOf(d).String(), stderrOf(d).String()
	if code != 0 || !strings.Contains(out, `+ new A "192.0.2.7"`) || !strings.Contains(out, "Result: success") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "Apply 1 operations to example.test? [y/N]") || strings.Contains(out, "[y/N]") {
		t.Fatalf("prompt: stdout %q stderr %q", out, errOut)
	}
}

func TestDryRunPlanDrift(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	api.set(func(f *fakeAPI) {
		z := f.zones["example.test"]
		z.RRs = append(z.RRs, fakeRR{Subname: "other", Rectype: "A", Content: "192.0.2.50", Prio: intp(0)})
	})
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--dry-run")
	if code != 4 || !t13HasError(e, "drift") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestDryRunPlanTampered(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	nb := strings.Replace(string(b), `"value": "192.0.2.1"`, `"value": "192.0.2.2"`, 1)
	if nb == string(b) || !strings.Contains(nb, `"192.0.2.1"`) {
		t.Fatalf("tamper did not apply to ops only: %s", nb)
	}
	if err := os.WriteFile(p, []byte(nb), 0o600); err != nil {
		t.Fatal(err)
	}
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--dry-run")
	if code != 4 || !t13HasError(e, "plan_mismatch") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestDryRunPlanVerifiedOK(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13Manifest)
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--dry-run")
	if code != 0 || e.Status != StatusSuccess || len(t13PlanOps(t, e)) != 2 || api.Mutations.Load() != 0 {
		t.Fatalf("exit %d %+v", code, e)
	}
}

func TestPlanDetailedExitcodeUsageErrorIsOne(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	f := t13File(t, "a.yaml", t13Manifest)
	for _, args := range [][]string{
		{"dns", "plan", "--file", "x", "--detailed-exitcode", "--bogus"},
		{"dns", "plan", "--file", f, "--detailed-exitcode", "extra"},
		{"dns", "plan", "--file", f, "--detailed-exitcode=true", "--max-deletes", "lots"},
	} {
		stdoutOf(d).Reset()
		if code := Execute(append([]string{"--output", "json"}, args...), d.Env, d); code != 1 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	if code := Execute([]string{"--output", "json", "dns", "plan", "--file", "x", "--detailed-exitcode=false", "--bogus"}, d.Env, d); code != 2 {
		t.Fatalf("flag off: exit %d", code)
	}
	if code := Execute([]string{"--output", "json", "dns", "apply", "--plan", "x", "--detailed-exitcode"}, d.Env, d); code != 2 {
		t.Fatalf("other command: exit %d", code)
	}
}

func TestApplyEmptySavedPlanVerifiesDrift(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13NoopManifest)
	api.set(func(f *fakeAPI) {
		z := f.zones["example.test"]
		z.RRs = append(z.RRs, fakeRR{Subname: "other", Rectype: "A", Content: "192.0.2.50", Prio: intp(0)})
	})
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--yes")
	if code != 4 || !t13HasError(e, "drift") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestApplyEmptySavedPlanNoDriftOK(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	p := t13Save(t, d, t13NoopManifest)
	code, e := t13Run(t, d, "dns", "apply", "--plan", p, "--yes")
	if code != 0 || e.Status != StatusSuccess {
		t.Fatalf("exit %d %+v", code, e)
	}
}

type cancelOnRead struct {
	cancel  func()
	release chan struct{}
}

func (r *cancelOnRead) Read([]byte) (int, error) {
	r.cancel()
	<-r.release
	return 0, errors.New("released")
}

func TestConfirmInterruptedByCancel(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.BaseContext = func() context.Context { return ctx }
	stdin := &cancelOnRead{cancel: cancel, release: make(chan struct{})}
	defer close(stdin.release)
	env := d.Env
	env.Stdin = stdin
	env.IsTTY = func() bool { return true }
	done := make(chan int, 1)
	go func() {
		done <- Execute([]string{"--output", "json", "dns", "add", "example.test", "new", "A", "192.0.2.7"}, env, d)
	}()
	var code int
	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation prompt ignores cancellation")
	}
	e := decodeOne(t, stdoutOf(d))
	if code != 1 || e.Status != StatusFailed || !t13HasError(e, "interrupted") {
		t.Fatalf("exit %d %+v", code, e)
	}
	if n := api.Mutations.Load(); n != 0 {
		t.Fatalf("mutations %d", n)
	}
}

func TestStdoutWriteFailureIsError(t *testing.T) {
	cases := []struct {
		name string
		args []string
		exit int
	}{
		{"version json", []string{"version", "--output", "json"}, 1},
		{"version text", []string{"version"}, 1},
		{"plan json", []string{"dns", "plan", "--output", "json", "--file", "M"}, 1},
		{"plan text detailed", []string{"dns", "plan", "--detailed-exitcode", "--file", "M"}, 1},
		{"plan json detailed", []string{"dns", "plan", "--output", "json", "--detailed-exitcode", "--file", "M"}, 1},
		{"fail json", []string{"dns", "plan", "--output", "json", "--file", "/nonexistent/zone.yaml"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDeps(t, newFakeAPI(t))
			f := t13File(t, "a.yaml", t13Manifest)
			args := append([]string{}, c.args...)
			for i, a := range args {
				if a == "M" {
					args[i] = f
				}
			}
			env := d.Env
			env.Stdout = failingWriter{}
			if code := Execute(args, env, d); code != c.exit {
				t.Fatalf("exit %d, stderr %q", code, stderrOf(d).String())
			}
			if !strings.Contains(stderrOf(d).String(), "broken pipe") {
				t.Fatalf("stderr %q", stderrOf(d).String())
			}
		})
	}
}

func TestTextWriteFailureKeepsWarnings(t *testing.T) {
	o := &Output{Env: Env{Stdout: failingWriter{}, Stderr: &bytes.Buffer{}}}
	code := o.Done(StatusSuccess, nil, []string{"careful"}, func(w io.Writer) error {
		_, err := io.WriteString(w, "x")
		return err
	})
	if code != ExitFailure || !strings.Contains(o.Env.Stderr.(*bytes.Buffer).String(), "warning: careful") {
		t.Fatalf("exit %d stderr %q", code, o.Env.Stderr)
	}
}

func TestConfirmShowsWarningsBeforePrompt(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	if code, e := t13Run(t, d, "dns", "add", "example.test", "c", "CNAME", "a.example.net.", "--yes"); code != 0 {
		t.Fatalf("seed: exit %d %+v", code, e)
	}
	code, _ := t13Exec(t, d, t13Opts{stdin: "n\n", tty: true}, "dns", "set", "example.test", "c", "CNAME", "b.example.net.")
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	s := stderrOf(d).String()
	w, p := strings.Index(s, "remove before add"), strings.Index(s, "[y/N]")
	if w < 0 || p < 0 || w > p {
		t.Fatalf("warning must precede prompt: %q", s)
	}
}
