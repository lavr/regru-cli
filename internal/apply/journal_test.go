package apply

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func journalDir(stateDir string) string {
	return filepath.Join(stateDir, "journal", "42", "example.test")
}

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func readJournal(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		if m["schema_version"] != float64(1) {
			t.Fatalf("schema_version missing: %v", m)
		}
		out = append(out, m)
	}
	return out
}

func TestJournalPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	j, err := OpenJournal(dir, "42", "example.test", "abcd")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if !regexp.MustCompile(`/journal/42/example\.test/\d{8}T\d{6}Z-abcd\.jsonl$`).MatchString(j.Path()) {
		t.Fatalf("path %s", j.Path())
	}
	st, err := os.Stat(j.Path())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", st.Mode().Perm())
	}
	for _, d := range []string{journalDir(dir), filepath.Dir(journalDir(dir)), filepath.Join(dir, "journal")} {
		st, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("dir %s mode %v", d, st.Mode().Perm())
		}
	}
	if _, err := OpenJournal(dir, "../x", "example.test", "abcd"); err == nil {
		t.Fatal("path traversal in user id accepted")
	}
	if _, err := OpenJournal(dir, "42", "example.test", "a/b"); err == nil {
		t.Fatal("path traversal in run id accepted")
	}
}

func TestJournalContent(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	dir := t.TempDir()
	j, err := OpenJournal(dir, "42", "example.test", "run7")
	if err != nil {
		t.Fatal(err)
	}
	Execute(context.Background(), f, j, p, vp, "run7")
	lines := readJournal(t, j.Path())
	begin := lines[0]
	if begin["plan_id"] != p.PlanID || begin["run_id"] != "run7" || begin["zone"] != "example.test" || begin["service_id"] != "100" {
		t.Fatalf("begin %v", begin)
	}
	acc := begin["account"].(map[string]any)
	if acc["user_id"] != "42" || acc["login"] != "user" || acc["alias"] != "main" {
		t.Fatalf("account %v", acc)
	}
	if len(begin["ops"].([]any)) != 2 {
		t.Fatalf("ops %v", begin["ops"])
	}
	if _, ok := begin["desired"].(map[string]any); !ok {
		t.Fatal("desired missing")
	}
	if _, ok := begin["policy"].(map[string]any); !ok {
		t.Fatal("policy missing")
	}
	res := lines[2]
	if res["status"] != "done" || res["op"].(map[string]any)["seq"] != float64(1) {
		t.Fatalf("result %v", res)
	}
	fin := lines[len(lines)-1]
	if fin["event"] != "finish" || fin["status"] != "success" {
		t.Fatalf("finish %v", fin)
	}
	if _, ok := fin["mismatch"]; !ok {
		t.Fatalf("finish without mismatch field: %v", fin)
	}
	if err := j.Finish("success", nil); err == nil {
		t.Fatal("write after finish accepted")
	}
}

func writeRun(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func finishLine(status string) string {
	return fmt.Sprintf(`{"schema_version":1,"event":"finish","status":%q,"mismatch":null}`, status) + "\n"
}

const beginLine = `{"schema_version":1,"event":"begin"}` + "\n"

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	var success []string
	for i := range 52 {
		name := fmt.Sprintf("20261018T1200%02dZ-s%02d.jsonl", i, i)
		success = append(success, name)
		writeRun(t, dir, name, beginLine+finishLine("success"))
	}
	writeRun(t, dir, "20261018T110000Z-unknown.jsonl", beginLine+finishLine("unknown"))
	writeRun(t, dir, "20261018T110001Z-open.jsonl", beginLine)
	torn := finishLine("success")
	writeRun(t, dir, "20261018T110002Z-torn.jsonl", beginLine+torn[:len(torn)-8])
	writeRun(t, dir, "20261018T110003Z-partial.jsonl", beginLine+finishLine("partial"))
	writeRun(t, dir, "notes.txt", "keep")
	if err := RotateJournals(dir, 50); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	for _, name := range success[:2] {
		if slices.Contains(left, name) {
			t.Fatalf("oldest %s not rotated", name)
		}
	}
	for _, name := range success[2:] {
		if !slices.Contains(left, name) {
			t.Fatalf("recent %s rotated", name)
		}
	}
	for _, name := range []string{"20261018T110000Z-unknown.jsonl", "20261018T110001Z-open.jsonl", "20261018T110002Z-torn.jsonl", "20261018T110003Z-partial.jsonl", "notes.txt"} {
		if !slices.Contains(left, name) {
			t.Fatalf("special %s removed", name)
		}
	}
	if len(left) != 55 {
		t.Fatalf("left %d: %s", len(left), strings.Join(left, ","))
	}
}

func TestRotateFailedCountsAsComplete(t *testing.T) {
	dir := t.TempDir()
	writeRun(t, dir, "20261018T120000Z-a.jsonl", beginLine+finishLine("failed"))
	writeRun(t, dir, "20261018T120001Z-b.jsonl", beginLine+finishLine("success"))
	if err := RotateJournals(dir, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20261018T120000Z-a.jsonl")); !os.IsNotExist(err) {
		t.Fatal("old failed run kept")
	}
	if err := RotateJournals(filepath.Join(dir, "missing"), 1); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}
