package apply

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

func testRules() plan.Rules {
	rw := zone.Cap{Read: true, Write: true, ExactDelete: true}
	return plan.Rules{
		Caps: map[zone.Type]zone.Cap{
			zone.A: rw, zone.AAAA: rw, zone.CNAME: rw, zone.MX: rw,
			zone.TXT: rw, zone.NS: rw, zone.SRV: rw, zone.CAA: rw,
			zone.HTTPS: {Read: true},
		},
		Protected: func(k zone.Key) bool { return k.Type == zone.NS && k.Name == "@" },
	}
}

var (
	recOld     = zone.Record{Name: "www", Type: zone.A, Value: "192.0.2.10"}
	recNew     = zone.Record{Name: "www", Type: zone.A, Value: "192.0.2.11"}
	recForeign = zone.Record{Name: "mail", Type: zone.A, Value: "192.0.2.20"}
)

func baseSnap() zone.Snapshot {
	return zone.Snapshot{
		Zone: "example.test",
		Records: []zone.Record{
			{Name: "@", Type: zone.NS, Value: "ns1.reg.ru."},
			{Name: "@", Type: zone.NS, Value: "ns2.reg.ru."},
			recOld,
			recForeign,
		},
		SOA: &zone.SOA{TTL: 3600, MinimumTTL: 300},
	}
}

func prepare(t *testing.T, want plan.Desired) plan.Prepared {
	t.Helper()
	acc := plan.Account{Alias: "main", Login: "user", UserID: "42"}
	p, err := plan.Prepare(acc, "example.test", "100", baseSnap(), want, plan.Policy{MaxDeletes: 10, Rules: testRules()}, func() string { return "00112233aabbccdd" })
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func setPrepared(t *testing.T) plan.Prepared {
	t.Helper()
	want, err := plan.DesiredSet(recOld.Key(), []string{recNew.Value})
	if err != nil {
		t.Fatal(err)
	}
	p := prepare(t, want)
	if len(p.Plan.Ops) != 2 || p.Plan.Ops[0].Kind != plan.OpAdd || p.Plan.Ops[1].Kind != plan.OpRemove {
		t.Fatalf("unexpected ops %+v", p.Plan.Ops)
	}
	return p
}

func addPrepared(t *testing.T) plan.Prepared {
	t.Helper()
	p := prepare(t, plan.DesiredAdd(baseSnap(), recNew.Key(), []string{recNew.Value}))
	if len(p.Plan.Ops) != 1 {
		t.Fatalf("unexpected ops %+v", p.Plan.Ops)
	}
	return p
}

type fakeBackend struct {
	mu                 sync.Mutex
	userID, serviceID  string
	snap               zone.Snapshot
	identityCalls      int
	snapshotCalls      int
	addCalls           int
	removeCalls        int
	failOn             map[zone.Record]error
	applyThenFail      map[zone.Record]error
	blockUntilCancel   map[zone.Record]bool
	applyBeforeBlock   bool
	drop               bool
	started            chan struct{}
	afterMutate        func(f *fakeBackend, r zone.Record)
	failSnapshotFromNo int
}

func newFake() *fakeBackend {
	return &fakeBackend{userID: "42", serviceID: "100", snap: baseSnap(), started: make(chan struct{}, 1)}
}

func (f *fakeBackend) Identity(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identityCalls++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f.userID, nil
}

func (f *fakeBackend) Snapshot(ctx context.Context) (zone.Snapshot, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotCalls++
	if err := ctx.Err(); err != nil {
		return zone.Snapshot{}, "", err
	}
	if f.failSnapshotFromNo > 0 && f.snapshotCalls >= f.failSnapshotFromNo {
		return zone.Snapshot{}, "", errors.New("read failed")
	}
	s := f.snap
	s.Records = slices.Clone(f.snap.Records)
	s.Raw = slices.Clone(f.snap.Raw)
	if f.snap.SOA != nil {
		soa := *f.snap.SOA
		s.SOA = &soa
	}
	return s, f.serviceID, nil
}

func (f *fakeBackend) apply(add bool, r zone.Record) {
	if add {
		if !slices.Contains(f.snap.Records, r) {
			f.snap.Records = append(f.snap.Records, r)
		}
		return
	}
	f.snap.Records = slices.DeleteFunc(f.snap.Records, func(x zone.Record) bool { return x == r })
}

func (f *fakeBackend) mutate(ctx context.Context, add bool, r zone.Record) error {
	f.mu.Lock()
	if add {
		f.addCalls++
	} else {
		f.removeCalls++
	}
	if f.blockUntilCancel[r] {
		if f.applyBeforeBlock {
			f.apply(add, r)
		}
		f.mu.Unlock()
		f.started <- struct{}{}
		<-ctx.Done()
		return fmt.Errorf("request interrupted: %w", ctx.Err())
	}
	defer f.mu.Unlock()
	if err := f.failOn[r]; err != nil {
		return err
	}
	if !f.drop {
		f.apply(add, r)
	}
	if f.afterMutate != nil {
		f.afterMutate(f, r)
	}
	if err := f.applyThenFail[r]; err != nil {
		return err
	}
	return nil
}

func (f *fakeBackend) Add(ctx context.Context, r zone.Record) error { return f.mutate(ctx, true, r) }

func (f *fakeBackend) Remove(ctx context.Context, r zone.Record) error {
	return f.mutate(ctx, false, r)
}

func (f *fakeBackend) mutations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addCalls + f.removeCalls
}

type memJournal struct {
	mu           sync.Mutex
	events       []string
	intentOps    []plan.Op
	failBegin    bool
	failIntentNo int
	failResultNo int
	failFinish   bool
	intents      int
	results      int
	finishStatus string
}

func (j *memJournal) Begin(p plan.Prepared, runID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failBegin {
		return errors.New("disk full")
	}
	j.events = append(j.events, "begin")
	return nil
}

func (j *memJournal) Intent(op plan.Op) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.intents++
	if j.intents == j.failIntentNo {
		return errors.New("disk full")
	}
	j.events = append(j.events, fmt.Sprintf("intent %d", op.Seq))
	j.intentOps = append(j.intentOps, op)
	return nil
}

func (j *memJournal) Result(op plan.Op, status, detail string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.results++
	if j.results == j.failResultNo {
		return errors.New("disk full")
	}
	j.events = append(j.events, fmt.Sprintf("result %d %s", op.Seq, status))
	return nil
}

func (j *memJournal) Finish(status string, final any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failFinish {
		return errors.New("disk full")
	}
	j.finishStatus = status
	j.events = append(j.events, "finish "+status)
	return nil
}

func staleCode(err error) string {
	var se *StaleError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func verified(t *testing.T, f *fakeBackend, p plan.Prepared) plan.Plan {
	t.Helper()
	vp, err := Verify(context.Background(), f, p, testRules())
	if err != nil {
		t.Fatal(err)
	}
	return vp
}

func statuses(r Result) []string {
	var out []string
	for _, o := range r.Ops {
		out = append(out, o.Status)
	}
	return out
}

func TestVerifyOK(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	if !slices.EqualFunc(vp.Ops, p.Plan.Ops, opEqual) {
		t.Fatalf("ops differ: %+v vs %+v", vp.Ops, p.Plan.Ops)
	}
	if !slices.Contains(vp.Target.Records, recNew) || slices.Contains(vp.Target.Records, recOld) {
		t.Fatalf("target not rebuilt: %+v", vp.Target)
	}
	if f.identityCalls != 1 || f.snapshotCalls != 1 || f.mutations() != 0 {
		t.Fatalf("calls: identity %d snapshot %d mutations %d", f.identityCalls, f.snapshotCalls, f.mutations())
	}
}

func TestVerifyOKShuffledSnapshot(t *testing.T) {
	f := newFake()
	slices.Reverse(f.snap.Records)
	verified(t, f, setPrepared(t))
}

func TestVerifyDrift(t *testing.T) {
	f := newFake()
	f.snap.Records = append(f.snap.Records, zone.Record{Name: "ftp", Type: zone.A, Value: "192.0.2.30"})
	_, err := Verify(context.Background(), f, setPrepared(t), testRules())
	if staleCode(err) != "drift" {
		t.Fatalf("want drift, got %v", err)
	}
}

func TestVerifyIdentityMismatch(t *testing.T) {
	f := newFake()
	f.userID = "43"
	_, err := Verify(context.Background(), f, setPrepared(t), testRules())
	if staleCode(err) != "identity_mismatch" {
		t.Fatalf("want identity_mismatch, got %v", err)
	}
	if f.snapshotCalls != 0 {
		t.Fatal("snapshot read after identity mismatch")
	}
}

func TestVerifyServiceMismatch(t *testing.T) {
	f := newFake()
	f.serviceID = "101"
	_, err := Verify(context.Background(), f, setPrepared(t), testRules())
	if staleCode(err) != "service_mismatch" {
		t.Fatalf("want service_mismatch, got %v", err)
	}
}

func TestVerifyEndpointNoNetwork(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	p.Endpoint = "https://api.example.net/api/regru2/"
	_, err := Verify(context.Background(), f, p, testRules())
	if staleCode(err) != "endpoint_mismatch" {
		t.Fatalf("want endpoint_mismatch, got %v", err)
	}
	if f.identityCalls+f.snapshotCalls+f.mutations() != 0 {
		t.Fatal("network calls before endpoint check")
	}
}

func TestVerifyPlanMismatch(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	ops := slices.Clone(p.Plan.Ops)
	ops[0].Record.Value = "192.0.2.99"
	p.Plan.Ops = ops
	_, err := Verify(context.Background(), f, p, testRules())
	if staleCode(err) != "plan_mismatch" {
		t.Fatalf("want plan_mismatch, got %v", err)
	}
	if f.mutations() != 0 {
		t.Fatal("mutations during verify")
	}
	p2 := setPrepared(t)
	p2.Plan.Ops = p2.Plan.Ops[:1]
	if _, err := Verify(context.Background(), newFake(), p2, testRules()); staleCode(err) != "plan_mismatch" {
		t.Fatalf("dropped op: want plan_mismatch, got %v", err)
	}
	p3 := setPrepared(t)
	p3.Plan.Ops = slices.Clone(p3.Plan.Ops)
	p3.Plan.Ops[1].DependsOn = nil
	if _, err := Verify(context.Background(), newFake(), p3, testRules()); staleCode(err) != "plan_mismatch" {
		t.Fatalf("changed deps: want plan_mismatch, got %v", err)
	}
}

func TestVerifyPolicyFromFile(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	p.Policy.MaxDeletes = 0
	_, err := Verify(context.Background(), f, p, testRules())
	var pe *plan.PolicyError
	if !errors.As(err, &pe) || pe.Code != "max_deletes_exceeded" {
		t.Fatalf("want max_deletes_exceeded, got %v", err)
	}
}

func TestExecuteSuccess(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	dir := t.TempDir()
	j, err := OpenJournal(dir, "42", "example.test", "run1")
	if err != nil {
		t.Fatal(err)
	}
	r := Execute(context.Background(), f, j, p, vp, "run1")
	if r.Status != "success" || r.JournalErr != nil || r.Mismatch != nil {
		t.Fatalf("result %+v", r)
	}
	if !slices.Equal(statuses(r), []string{"done", "done"}) {
		t.Fatalf("statuses %v", statuses(r))
	}
	lines := readJournal(t, j.Path())
	var events []string
	for _, l := range lines {
		events = append(events, l["event"].(string))
	}
	if !slices.Equal(events, []string{"begin", "intent", "result", "intent", "result", "finish"}) {
		t.Fatalf("events %v", events)
	}
	if lines[5]["status"] != "success" {
		t.Fatalf("finish %v", lines[5])
	}
}

func TestExecuteAddFailsStopsRemoves(t *testing.T) {
	f := newFake()
	f.failOn = map[zone.Record]error{recNew: &regruapi.APIError{Code: "SOME_ERROR"}}
	p := setPrepared(t)
	vp := verified(t, f, p)
	j := &memJournal{}
	r := Execute(context.Background(), f, j, p, vp, "run1")
	if !slices.Equal(statuses(r), []string{"failed", "skipped"}) || r.Status != "failed" {
		t.Fatalf("result %+v", r)
	}
	if f.removeCalls != 0 {
		t.Fatal("remove sent after failed add")
	}
	if r.Ops[0].Detail == "" {
		t.Fatal("detail missing")
	}
	if j.finishStatus != "failed" {
		t.Fatalf("finish %q", j.finishStatus)
	}
}

func TestUnknownConfirmed(t *testing.T) {
	f := newFake()
	f.applyThenFail = map[zone.Record]error{recNew: &regruapi.UnknownOutcomeError{Method: "zone/add_alias", Err: errors.New("timeout")}}
	p := setPrepared(t)
	vp := verified(t, f, p)
	r := Execute(context.Background(), f, &memJournal{}, p, vp, "run1")
	if f.addCalls != 1 {
		t.Fatalf("add calls %d", f.addCalls)
	}
	if !slices.Equal(statuses(r), []string{"done", "done"}) || r.Status != "success" {
		t.Fatalf("result %+v", r)
	}
}

func TestUnknownUnconfirmed(t *testing.T) {
	f := newFake()
	f.failOn = map[zone.Record]error{recNew: &regruapi.UnknownOutcomeError{Method: "zone/add_alias", Err: errors.New("timeout")}}
	p := setPrepared(t)
	vp := verified(t, f, p)
	j := &memJournal{}
	r := Execute(context.Background(), f, j, p, vp, "run1")
	if f.addCalls != 1 || f.removeCalls != 0 {
		t.Fatalf("add %d remove %d", f.addCalls, f.removeCalls)
	}
	if !slices.Equal(statuses(r), []string{"unknown", "skipped"}) || r.Status != "unknown" {
		t.Fatalf("result %+v", r)
	}
	if j.finishStatus != "unknown" {
		t.Fatalf("finish %q", j.finishStatus)
	}
}

func TestCancelBetweenSteps(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.afterMutate = func(*fakeBackend, zone.Record) { cancel() }
	r := Execute(ctx, f, &memJournal{}, p, vp, "run1")
	if !slices.Equal(statuses(r), []string{"done", "skipped"}) {
		t.Fatalf("statuses %v", statuses(r))
	}
	if r.Status != "partial" || r.Mismatch != nil {
		t.Fatalf("result %+v mismatch %+v", r, r.Mismatch)
	}
	if f.removeCalls != 0 {
		t.Fatal("remove sent after cancel")
	}
}

func TestCancelBetweenStepsForeignChange(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.afterMutate = func(f *fakeBackend, _ zone.Record) {
		f.apply(false, recForeign)
		cancel()
	}
	r := Execute(ctx, f, &memJournal{}, p, vp, "run1")
	if r.Status != "unknown" || r.Mismatch == nil {
		t.Fatalf("result %+v", r)
	}
	if !slices.Contains(r.Mismatch.Missing, recForeign) {
		t.Fatalf("mismatch %+v", r.Mismatch)
	}
}

func TestCancelInFlight(t *testing.T) {
	f := newFake()
	f.blockUntilCancel = map[zone.Record]bool{recNew: true}
	p := setPrepared(t)
	vp := verified(t, f, p)
	dir := t.TempDir()
	j, err := OpenJournal(dir, "42", "example.test", "run1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-f.started
		cancel()
	}()
	r := Execute(ctx, f, j, p, vp, "run1")
	if !slices.Equal(statuses(r), []string{"unknown", "skipped"}) || r.Status != "unknown" {
		t.Fatalf("result %+v", r)
	}
	if f.addCalls != 1 || f.removeCalls != 0 {
		t.Fatalf("add %d remove %d", f.addCalls, f.removeCalls)
	}
	lines := readJournal(t, j.Path())
	var intent map[string]any
	for _, l := range lines {
		if l["event"] == "intent" {
			intent = l
		}
	}
	if intent == nil {
		t.Fatal("no intent")
	}
	op := intent["op"].(map[string]any)
	if op["name"] != "www" || op["type"] != "A" || op["value"] != "192.0.2.11" || op["op"] != "add" {
		t.Fatalf("intent %v", intent)
	}
	last := lines[len(lines)-1]
	if last["event"] != "finish" || last["status"] != "unknown" {
		t.Fatalf("finish %v", last)
	}
}

func TestCancelInFlightConfirmedByControlRead(t *testing.T) {
	f := newFake()
	f.blockUntilCancel = map[zone.Record]bool{recNew: true}
	f.applyBeforeBlock = true
	p := setPrepared(t)
	vp := verified(t, f, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-f.started
		cancel()
	}()
	r := Execute(ctx, f, &memJournal{}, p, vp, "run1")
	if !slices.Equal(statuses(r), []string{"done", "skipped"}) || r.Status != "partial" || r.Mismatch != nil {
		t.Fatalf("result %+v", r)
	}
}

func TestVerificationMismatch(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	f.drop = true
	r := Execute(context.Background(), f, &memJournal{}, p, vp, "run1")
	if r.Status != "unknown" || r.Mismatch == nil {
		t.Fatalf("result %+v", r)
	}
	if !slices.Contains(r.Mismatch.Missing, recNew) || !slices.Contains(r.Mismatch.Unexpected, recOld) {
		t.Fatalf("mismatch %+v", r.Mismatch)
	}
}

func TestUnexpectedChange(t *testing.T) {
	f := newFake()
	p := addPrepared(t)
	vp := verified(t, f, p)
	f.afterMutate = func(f *fakeBackend, _ zone.Record) { f.apply(false, recForeign) }
	r := Execute(context.Background(), f, &memJournal{}, p, vp, "run1")
	if r.Status != "unknown" || r.Mismatch == nil || !slices.Contains(r.Mismatch.Missing, recForeign) {
		t.Fatalf("result %+v mismatch %+v", r, r.Mismatch)
	}
}

func TestRawAndSOAChanges(t *testing.T) {
	f := newFake()
	p := addPrepared(t)
	vp := verified(t, f, p)
	f.afterMutate = func(f *fakeBackend, _ zone.Record) {
		f.snap.Raw = append(f.snap.Raw, zone.Raw{Name: "x", Type: "HTTPS", Content: "1 .", Count: 1})
		f.snap.SOA = &zone.SOA{TTL: 1, MinimumTTL: 1}
	}
	r := Execute(context.Background(), f, &memJournal{}, p, vp, "run1")
	if r.Status != "unknown" || r.Mismatch == nil || !r.Mismatch.RawChanged || !r.Mismatch.SOAChanged {
		t.Fatalf("result %+v mismatch %+v", r, r.Mismatch)
	}
}

func TestFinalReadFails(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	f.failSnapshotFromNo = 2
	r := Execute(context.Background(), f, &memJournal{}, p, vp, "run1")
	if !slices.Equal(statuses(r), []string{"done", "done"}) || r.Status != "unknown" {
		t.Fatalf("result %+v", r)
	}
}

func TestBeginWriteFailsNoRequest(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	r := Execute(context.Background(), f, &memJournal{failBegin: true}, p, vp, "run1")
	if f.mutations() != 0 || r.JournalErr == nil || r.Status != "failed" {
		t.Fatalf("result %+v mutations %d", r, f.mutations())
	}
	if !slices.Equal(statuses(r), []string{"skipped", "skipped"}) {
		t.Fatalf("statuses %v", statuses(r))
	}
}

func TestIntentWriteFailsNoRequest(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	r := Execute(context.Background(), f, &memJournal{failIntentNo: 1}, p, vp, "run1")
	if f.mutations() != 0 || r.JournalErr == nil || r.Status != "failed" {
		t.Fatalf("result %+v mutations %d", r, f.mutations())
	}
	if !slices.Equal(statuses(r), []string{"skipped", "skipped"}) {
		t.Fatalf("statuses %v", statuses(r))
	}
}

func TestResultWriteFailsStops(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	r := Execute(context.Background(), f, &memJournal{failResultNo: 1}, p, vp, "run1")
	if f.removeCalls != 0 {
		t.Fatal("second operation sent after result write failure")
	}
	if !slices.Equal(statuses(r), []string{"done", "skipped"}) || r.Status != "partial" || r.JournalErr == nil {
		t.Fatalf("result %+v", r)
	}
}

func TestResultWriteFailsOnLastOp(t *testing.T) {
	f := newFake()
	p := addPrepared(t)
	vp := verified(t, f, p)
	r := Execute(context.Background(), f, &memJournal{failResultNo: 1}, p, vp, "run1")
	if !slices.Equal(statuses(r), []string{"done"}) || r.Status != "success" || r.JournalErr == nil {
		t.Fatalf("result %+v", r)
	}
}

type finishFails struct{ *FileJournal }

func (j finishFails) Finish(string, any) error {
	j.FileJournal.Close()
	return errors.New("disk full")
}

func TestFinishWriteFails(t *testing.T) {
	f := newFake()
	p := setPrepared(t)
	vp := verified(t, f, p)
	dir := t.TempDir()
	fj, err := OpenJournal(dir, "42", "example.test", "run1")
	if err != nil {
		t.Fatal(err)
	}
	r := Execute(context.Background(), f, finishFails{fj}, p, vp, "run1")
	if r.Status != "success" || r.JournalErr == nil {
		t.Fatalf("result %+v", r)
	}
	if !slices.Contains(f.snap.Records, recNew) || slices.Contains(f.snap.Records, recOld) {
		t.Fatal("plan not applied")
	}
	if err := RotateJournals(journalDir(dir), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readFile(fj.Path()); err != nil {
		t.Fatalf("journal without finish was rotated: %v", err)
	}
}

func TestExecuteEmptyPlan(t *testing.T) {
	f := newFake()
	p := prepare(t, plan.Desired{Scope: plan.ScopeListed, RRsets: map[zone.Key][]string{recOld.Key(): {recOld.Value}}})
	vp := verified(t, f, p)
	r := Execute(context.Background(), f, &memJournal{}, p, vp, "run1")
	if r.Status != "success" || len(r.Ops) != 0 || f.snapshotCalls != 1 {
		t.Fatalf("result %+v snapshots %d", r, f.snapshotCalls)
	}
}
