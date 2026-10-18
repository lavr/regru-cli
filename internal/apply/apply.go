package apply

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

const (
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusUnknown = "unknown"
	StatusSkipped = "skipped"

	RunSuccess = "success"
	RunFailed  = "failed"
	RunPartial = "partial"
	RunUnknown = "unknown"

	controlReadTimeout = 30 * time.Second
)

type Backend interface {
	Identity(ctx context.Context) (string, error)
	Snapshot(ctx context.Context) (zone.Snapshot, string, error)
	Add(ctx context.Context, r zone.Record) error
	Remove(ctx context.Context, r zone.Record) error
}

type StaleError struct {
	Code   string
	Detail any
}

func (e *StaleError) Error() string {
	if e.Detail == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Detail)
}

type Journal interface {
	Begin(p plan.Prepared, runID string) error
	Intent(op plan.Op) error
	Result(op plan.Op, status, detail string) error
	Finish(status string, final any) error
}

type OpResult struct {
	Op     plan.Op
	Status string
	Detail string
}

type Mismatch struct {
	Missing    []zone.Record
	Unexpected []zone.Record
	RawChanged bool
	SOAChanged bool
}

type jsonRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

func jsonRecords(rs []zone.Record) []jsonRecord {
	out := []jsonRecord{}
	for _, r := range rs {
		out = append(out, jsonRecord{Name: r.Name, Type: string(r.Type), Value: r.Value})
	}
	return out
}

func (m Mismatch) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Missing    []jsonRecord `json:"missing"`
		Unexpected []jsonRecord `json:"unexpected"`
		RawChanged bool         `json:"raw_changed"`
		SOAChanged bool         `json:"soa_changed"`
	}{jsonRecords(m.Missing), jsonRecords(m.Unexpected), m.RawChanged, m.SOAChanged})
}

type Final struct {
	Mismatch  *Mismatch
	ReadError string
}

type Result struct {
	Status     string
	Ops        []OpResult
	Mismatch   *Mismatch
	JournalErr error
}

func opEqual(a, b plan.Op) bool {
	return a.Seq == b.Seq && a.Kind == b.Kind && a.Record == b.Record && slices.Equal(a.DependsOn, b.DependsOn)
}

func Verify(ctx context.Context, b Backend, p plan.Prepared, rules plan.Rules) (plan.Plan, error) {
	if p.Endpoint != regruapi.Endpoint {
		return plan.Plan{}, &StaleError{Code: "endpoint_mismatch", Detail: map[string]string{"plan": p.Endpoint, "expected": regruapi.Endpoint}}
	}
	uid, err := b.Identity(ctx)
	if err != nil {
		return plan.Plan{}, err
	}
	if uid != p.Account.UserID {
		return plan.Plan{}, &StaleError{Code: "identity_mismatch", Detail: map[string]string{"plan": p.Account.UserID, "actual": uid}}
	}
	snap, sid, err := b.Snapshot(ctx)
	if err != nil {
		return plan.Plan{}, err
	}
	if sid != p.ServiceID {
		return plan.Plan{}, &StaleError{Code: "service_mismatch", Detail: map[string]string{"plan": p.ServiceID, "actual": sid}}
	}
	if snap.Zone != p.Zone {
		return plan.Plan{}, &StaleError{Code: "drift", Detail: map[string]string{"plan_zone": p.Zone, "actual_zone": snap.Zone}}
	}
	if fp := plan.Fingerprint(p.Zone, sid, snap); fp != p.BaselineFingerprint {
		return plan.Plan{}, &StaleError{Code: "drift", Detail: map[string]string{"baseline": p.BaselineFingerprint, "actual": fp}}
	}
	pol := plan.Policy{MaxDeletes: p.Policy.MaxDeletes, AllowEmptyZone: p.Policy.AllowEmptyZone, Rules: rules}
	rebuilt, err := plan.Build(snap, p.Desired, pol)
	if err != nil {
		return plan.Plan{}, err
	}
	if !slices.EqualFunc(rebuilt.Ops, p.Plan.Ops, opEqual) {
		return plan.Plan{}, &StaleError{Code: "plan_mismatch", Detail: "operations rebuilt from the current zone differ from the confirmed plan"}
	}
	return rebuilt, nil
}

func unknownOutcome(err error) bool {
	var u interface{ UnknownOutcome() bool }
	return errors.As(err, &u) && u.UnknownOutcome()
}

func controlContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), controlReadTimeout)
}

func postcondition(s zone.Snapshot, op plan.Op) bool {
	present := slices.Contains(s.Records, op.Record)
	if op.Kind == plan.OpAdd {
		return present
	}
	return !present
}

func Execute(ctx context.Context, b Backend, j Journal, prep plan.Prepared, p plan.Plan, runID string) Result {
	res := Result{Ops: make([]OpResult, len(p.Ops))}
	for i, op := range p.Ops {
		res.Ops[i] = OpResult{Op: op, Status: StatusSkipped}
	}
	if err := j.Begin(prep, runID); err != nil {
		res.Status = RunFailed
		res.JournalErr = err
		return res
	}
	var journalErrs []error
	for i, op := range p.Ops {
		if ctx.Err() != nil {
			break
		}
		if err := j.Intent(op); err != nil {
			journalErrs = append(journalErrs, err)
			break
		}
		status, detail := runOp(ctx, b, op)
		res.Ops[i].Status, res.Ops[i].Detail = status, detail
		if err := j.Result(op, status, detail); err != nil {
			journalErrs = append(journalErrs, err)
			break
		}
		if status != StatusDone {
			break
		}
	}

	done, unknown := 0, false
	var applied []plan.Op
	for _, r := range res.Ops {
		switch r.Status {
		case StatusDone:
			done++
			applied = append(applied, r.Op)
		case StatusUnknown:
			unknown = true
		}
	}

	final := Final{}
	readFailed := false
	if done > 0 || unknown {
		cctx, cancel := controlContext(ctx)
		snap, _, err := b.Snapshot(cctx)
		cancel()
		if err != nil {
			readFailed = true
			final.ReadError = err.Error()
		} else {
			res.Mismatch = compare(expected(p.Initial, applied), snap)
			final.Mismatch = res.Mismatch
		}
	}

	switch {
	case unknown || res.Mismatch != nil || readFailed:
		res.Status = RunUnknown
	case done == len(res.Ops):
		res.Status = RunSuccess
	case done == 0:
		res.Status = RunFailed
	default:
		res.Status = RunPartial
	}

	if err := j.Finish(res.Status, final); err != nil {
		journalErrs = append(journalErrs, err)
	}
	res.JournalErr = errors.Join(journalErrs...)
	return res
}

func runOp(ctx context.Context, b Backend, op plan.Op) (string, string) {
	var err error
	switch op.Kind {
	case plan.OpAdd:
		err = b.Add(ctx, op.Record)
	case plan.OpRemove:
		err = b.Remove(ctx, op.Record)
	default:
		return StatusFailed, fmt.Sprintf("unsupported operation %q", op.Kind)
	}
	if err == nil {
		return StatusDone, ""
	}
	interrupted := ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if !unknownOutcome(err) && !interrupted {
		return StatusFailed, err.Error()
	}
	cctx, cancel := controlContext(ctx)
	defer cancel()
	snap, _, rerr := b.Snapshot(cctx)
	if rerr != nil {
		return StatusUnknown, fmt.Sprintf("%v; control read failed: %v", err, rerr)
	}
	if postcondition(snap, op) {
		return StatusDone, fmt.Sprintf("%v; confirmed by control read", err)
	}
	return StatusUnknown, fmt.Sprintf("%v; control read does not show the change", err)
}

func expected(initial zone.Snapshot, applied []plan.Op) zone.Snapshot {
	out := initial
	out.Records = slices.Clone(initial.Records)
	for _, op := range applied {
		switch op.Kind {
		case plan.OpAdd:
			if !slices.Contains(out.Records, op.Record) {
				out.Records = append(out.Records, op.Record)
			}
		case plan.OpRemove:
			out.Records = slices.DeleteFunc(out.Records, func(r zone.Record) bool { return r == op.Record })
		}
	}
	return out
}

type rawKey struct {
	Name, Type, Content string
	HasPrio             bool
	Prio, Count         int
}

func rawCounts(rs []zone.Raw) map[rawKey]int {
	m := map[rawKey]int{}
	for _, r := range rs {
		k := rawKey{Name: r.Name, Type: r.Type, Content: r.Content, Count: r.Count}
		if r.Prio != nil {
			k.HasPrio, k.Prio = true, *r.Prio
		}
		m[k]++
	}
	return m
}

func recordCounts(rs []zone.Record) map[zone.Record]int {
	m := map[zone.Record]int{}
	for _, r := range rs {
		m[r]++
	}
	return m
}

func diff(a, b map[zone.Record]int) []zone.Record {
	var out []zone.Record
	for r, n := range a {
		for range n - b[r] {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(x, y zone.Record) int {
		switch {
		case x.Name != y.Name:
			return cmp.Compare(x.Name, y.Name)
		case x.Type != y.Type:
			return cmp.Compare(string(x.Type), string(y.Type))
		default:
			return cmp.Compare(x.Value, y.Value)
		}
	})
	return out
}

func soaEqual(a, b *zone.SOA) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func mapsEqual[K comparable](a, b map[K]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func compare(want, got zone.Snapshot) *Mismatch {
	w, g := recordCounts(want.Records), recordCounts(got.Records)
	m := Mismatch{
		Missing:    diff(w, g),
		Unexpected: diff(g, w),
		RawChanged: !mapsEqual(rawCounts(want.Raw), rawCounts(got.Raw)),
		SOAChanged: !soaEqual(want.SOA, got.SOA),
	}
	if len(m.Missing) == 0 && len(m.Unexpected) == 0 && !m.RawChanged && !m.SOAChanged {
		return nil
	}
	return &m
}
