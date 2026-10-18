package regruzone

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
	return ctx.Err()
}

type call struct {
	Path  string
	Input map[string]any
}

type fakeAPI struct {
	mu    sync.Mutex
	calls []call
	reply func(path string) (int, string)
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var in map[string]any
	json.Unmarshal([]byte(r.PostForm.Get("input_data")), &in)
	f.mu.Lock()
	f.calls = append(f.calls, call{Path: r.URL.Path, Input: in})
	f.mu.Unlock()
	code, body := f.reply(r.URL.Path)
	w.WriteHeader(code)
	w.Write([]byte(body))
}

func (f *fakeAPI) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func newBackend(t *testing.T, reply func(path string) (int, string)) (*Backend, *fakeAPI) {
	t.Helper()
	f := &fakeAPI{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	clk := &fakeClock{now: time.Unix(1000, 0)}
	c := regruapi.New(regruapi.Credentials{Username: "user", Password: "pw"}, regruapi.NewLimiter(3*time.Second, clk),
		regruapi.WithBaseURL(srv.URL), regruapi.WithClock(clk), regruapi.WithMaxAttempts(1))
	return NewBackend(c, "example.test"), f
}

func byPath(t *testing.T, m map[string]string) func(string) (int, string) {
	return func(path string) (int, string) {
		name, ok := m[path]
		if !ok {
			return 500, "unexpected"
		}
		if strings.HasPrefix(name, "{") {
			return 200, name
		}
		return 200, fixtureResponse(t, name)
	}
}

func codeOf(err error) string {
	var ce interface{ Code() string }
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return ""
}

func TestBackendRemoveNeedsSnapshot(t *testing.T) {
	b, f := newBackend(t, byPath(t, map[string]string{}))
	err := b.Remove(context.Background(), zone.Record{Name: "www", Type: zone.A, Value: "192.0.2.200"})
	if codeOf(err) != "no_selector" {
		t.Fatalf("err %v", err)
	}
	if n := len(f.recorded()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestBackendIdentity(t *testing.T) {
	b, _ := newBackend(t, byPath(t, map[string]string{"/nop": "nop_success"}))
	id, err := b.Identity(context.Background())
	if err != nil || id != "1000" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestBackendSnapshotAndRemove(t *testing.T) {
	b, f := newBackend(t, byPath(t, map[string]string{
		"/zone/get_resource_records": "zone_get_resource_records_caa_pair",
		"/zone/remove_record":        "zone_remove_record_success",
	}))
	ctx := context.Background()
	s, sid, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "1000001" || s.Zone != "example.test" || len(s.Records) != 6 {
		t.Fatalf("%q %#v", sid, s)
	}
	r := zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue a.example.net"}
	if err := b.Remove(ctx, r); err != nil {
		t.Fatal(err)
	}
	calls := f.recorded()
	last := calls[len(calls)-1]
	if last.Path != "/zone/remove_record" {
		t.Fatalf("%v", calls)
	}
	want := map[string]any{"subdomain": "caa1.probe", "record_type": "CAA", "priority": "0", "content": "0 issue \"a.example.net\""}
	for k, v := range want {
		if last.Input[k] != v {
			t.Fatalf("%s = %v, want %v (%v)", k, last.Input[k], v, last.Input)
		}
	}
	if err := b.Remove(ctx, zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue b.example.net"}); codeOf(err) != "no_selector" {
		t.Fatalf("err %v", err)
	}
	if n := len(f.recorded()); n != len(calls) {
		t.Fatalf("extra requests: %d", n-len(calls))
	}
}

func TestBackendSnapshotReplacesSelectors(t *testing.T) {
	var mu sync.Mutex
	fixture := "zone_get_resource_records_caa_pair"
	b, f := newBackend(t, func(path string) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if path == "/zone/get_resource_records" {
			return 200, fixtureResponse(t, fixture)
		}
		return 500, "unexpected"
	})
	ctx := context.Background()
	if _, _, err := b.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	fixture = "zone_get_resource_records_baseline"
	mu.Unlock()
	if _, _, err := b.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	n := len(f.recorded())
	err := b.Remove(ctx, zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue a.example.net"})
	if codeOf(err) != "no_selector" || len(f.recorded()) != n {
		t.Fatalf("err %v", err)
	}
}

func TestBackendSnapshotErrorKeepsNoStaleSelectors(t *testing.T) {
	var mu sync.Mutex
	fail := false
	b, f := newBackend(t, func(path string) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if path == "/zone/get_resource_records" && !fail {
			return 200, fixtureResponse(t, "zone_get_resource_records_caa_pair")
		}
		return 200, `{"result":"success","answer":{"domains":[]}}`
	})
	ctx := context.Background()
	if _, _, err := b.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if _, _, err := b.Snapshot(ctx); err == nil {
		t.Fatal("expected error")
	}
	n := len(f.recorded())
	err := b.Remove(ctx, zone.Record{Name: "caa1.probe", Type: zone.CAA, Value: "0 issue a.example.net"})
	if codeOf(err) != "no_selector" || len(f.recorded()) != n {
		t.Fatalf("err %v", err)
	}
}

func TestBackendAdd(t *testing.T) {
	b, f := newBackend(t, byPath(t, map[string]string{"/zone/add_srv": "zone_add_srv_success"}))
	if err := b.Add(context.Background(), zone.Record{Name: "_s1._tcp.probe", Type: zone.SRV, Value: "10 5 5060 sip.example.net."}); err != nil {
		t.Fatal(err)
	}
	c := f.recorded()
	if len(c) != 1 || c[0].Path != "/zone/add_srv" || c[0].Input["service"] != "_s1._tcp.probe" || c[0].Input["target"] != "sip.example.net." {
		t.Fatalf("%v", c)
	}
	if _, ok := c[0].Input["subdomain"]; ok {
		t.Fatalf("%v", c)
	}
}

func TestBackendAddRejectsWithoutRequest(t *testing.T) {
	b, f := newBackend(t, byPath(t, map[string]string{}))
	err := b.Add(context.Background(), zone.Record{Name: "c", Type: zone.CAA, Value: "0 contactemail a@example.net"})
	if codeOf(err) != "unsupported_value" || len(f.recorded()) != 0 {
		t.Fatalf("err %v", err)
	}
}

func TestBackendUnknownOutcomePassthrough(t *testing.T) {
	b, _ := newBackend(t, func(path string) (int, string) {
		if path == "/zone/get_resource_records" {
			return 200, fixtureResponse(t, "zone_get_resource_records_mx_pair")
		}
		return 502, "<html>bad gateway</html>"
	})
	ctx := context.Background()
	err := b.Add(ctx, zone.Record{Name: "a", Type: zone.A, Value: "192.0.2.9"})
	if _, ok := err.(*regruapi.UnknownOutcomeError); !ok {
		t.Fatalf("add err %T %v", err, err)
	}
	if _, _, err := b.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	err = b.Remove(ctx, zone.Record{Name: "mx1.probe", Type: zone.MX, Value: "10 mx-a.example.net."})
	if _, ok := err.(*regruapi.UnknownOutcomeError); !ok {
		t.Fatalf("remove err %T %v", err, err)
	}
}

func TestBackendDomainErrorPassthrough(t *testing.T) {
	b, _ := newBackend(t, byPath(t, map[string]string{"/zone/add_cname": "zone_add_cname_domain_error_conflict_cname"}))
	err := b.Add(context.Background(), zone.Record{Name: "c2.probe", Type: zone.CNAME, Value: "t1.example.net."})
	var de *regruapi.DomainError
	if !errors.As(err, &de) || de.Code != "CONFLICT_CNAME" {
		t.Fatalf("err %T %v", err, err)
	}
}
