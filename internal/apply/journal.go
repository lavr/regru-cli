package apply

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lavr/regru-cli/internal/plan"
)

const journalSchemaVersion = 1

type FileJournal struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	runID  string
	planID string
}

func safeComponent(kind, s string) error {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00") {
		return fmt.Errorf("invalid %s %q for a state path", kind, s)
	}
	return nil
}

func ensureDir(base string, parts ...string) (string, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	dir := base
	for _, p := range parts {
		dir = filepath.Join(dir, p)
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func OpenJournal(stateDir, userID, zoneName, runID string) (*FileJournal, error) {
	for _, c := range [][2]string{{"user id", userID}, {"zone", zoneName}, {"run id", runID}} {
		if err := safeComponent(c[0], c[1]); err != nil {
			return nil, err
		}
	}
	dir, err := ensureDir(stateDir, "journal", userID, zoneName)
	if err != nil {
		return nil, err
	}
	name := time.Now().UTC().Format("20060102T150405Z") + "-" + runID + ".jsonl"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := syncDir(dir); err != nil {
		f.Close()
		return nil, err
	}
	return &FileJournal{f: f, path: path, runID: runID}, nil
}

func (j *FileJournal) Path() string { return j.path }

func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	j.f = nil
	return err
}

func (j *FileJournal) write(event string, fields map[string]any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return errors.New("journal is closed")
	}
	line := map[string]any{"schema_version": journalSchemaVersion, "event": event, "time": time.Now().UTC().Format(time.RFC3339Nano), "run_id": j.runID}
	if j.planID != "" {
		line["plan_id"] = j.planID
	}
	for k, v := range fields {
		line[k] = v
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

type journalOp struct {
	Seq       int    `json:"seq"`
	Op        string `json:"op"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	DependsOn []int  `json:"depends_on"`
}

func toJournalOp(op plan.Op) journalOp {
	return journalOp{
		Seq: op.Seq, Op: string(op.Kind),
		Name: op.Record.Name, Type: string(op.Record.Type), Value: op.Record.Value,
		DependsOn: append([]int{}, op.DependsOn...),
	}
}

type journalRRset struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

func (j *FileJournal) Begin(p plan.Prepared, runID string) error {
	j.mu.Lock()
	j.planID = p.PlanID
	j.runID = runID
	j.mu.Unlock()
	ops := []journalOp{}
	for _, op := range p.Plan.Ops {
		ops = append(ops, toJournalOp(op))
	}
	rrsets := []journalRRset{}
	for k, v := range p.Desired.RRsets {
		rrsets = append(rrsets, journalRRset{Name: k.Name, Type: string(k.Type), Values: append([]string{}, v...)})
	}
	sort.Slice(rrsets, func(a, b int) bool {
		if rrsets[a].Name != rrsets[b].Name {
			return rrsets[a].Name < rrsets[b].Name
		}
		return rrsets[a].Type < rrsets[b].Type
	})
	return j.write("begin", map[string]any{
		"account":              map[string]string{"alias": p.Account.Alias, "login": p.Account.Login, "user_id": p.Account.UserID},
		"endpoint":             p.Endpoint,
		"zone":                 p.Zone,
		"service_id":           p.ServiceID,
		"baseline_fingerprint": p.BaselineFingerprint,
		"policy":               map[string]any{"max_deletes": p.Policy.MaxDeletes, "allow_empty_zone": p.Policy.AllowEmptyZone},
		"desired":              map[string]any{"scope": string(p.Desired.Scope), "rrsets": rrsets},
		"ops":                  ops,
	})
}

func (j *FileJournal) Intent(op plan.Op) error {
	return j.write("intent", map[string]any{"op": toJournalOp(op)})
}

func (j *FileJournal) Result(op plan.Op, status, detail string) error {
	return j.write("result", map[string]any{"op": toJournalOp(op), "status": status, "detail": detail})
}

func (j *FileJournal) Finish(status string, final any) error {
	fields := map[string]any{"status": status}
	switch f := final.(type) {
	case Final:
		fields["mismatch"] = f.Mismatch
		if f.ReadError != "" {
			fields["final_read_error"] = f.ReadError
		}
	case *Final:
		if f != nil {
			fields["mismatch"] = f.Mismatch
			if f.ReadError != "" {
				fields["final_read_error"] = f.ReadError
			}
		} else {
			fields["mismatch"] = nil
		}
	default:
		fields["mismatch"] = final
	}
	err := j.write("finish", fields)
	if cerr := j.Close(); err == nil && cerr != nil {
		err = cerr
	}
	return err
}

func completedRun(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
		return false
	}
	body := b[:len(b)-1]
	last := body[bytes.LastIndexByte(body, '\n')+1:]
	var line struct {
		SchemaVersion *int    `json:"schema_version"`
		Event         string  `json:"event"`
		Status        *string `json:"status"`
	}
	if err := json.Unmarshal(last, &line); err != nil {
		return false
	}
	if line.SchemaVersion == nil || *line.SchemaVersion != journalSchemaVersion || line.Event != "finish" || line.Status == nil {
		return false
	}
	return *line.Status == RunSuccess || *line.Status == RunFailed
}

func RotateJournals(dir string, keep int) error {
	if keep < 0 {
		return fmt.Errorf("keep must be >= 0, got %d", keep)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var done []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if completedRun(p) {
			done = append(done, e.Name())
		}
	}
	sort.Strings(done)
	if len(done) <= keep {
		return nil
	}
	var errs []error
	for _, name := range done[:len(done)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
