package regruapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return string(f.Response)
}

type respFn func(n int, w http.ResponseWriter, r *http.Request)

func newServer(t *testing.T, fn respFn) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fn(int(n.Add(1)), w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func body(s string) respFn {
	return func(n int, w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(s))
	}
}

func testClient(url string, clk Clock, opts ...Option) *Client {
	opts = append([]Option{WithBaseURL(url), WithClock(clk)}, opts...)
	return New(Credentials{Username: "user", Password: "s3cr3t-pass"}, NewLimiter(0, clk), opts...)
}

func TestCallFormShape(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	var form url.Values
	srv, _ := newServer(t, func(n int, w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotMethod = r.URL.Path, r.URL.RawQuery, r.Method
		r.ParseForm()
		form = r.PostForm
		w.Write([]byte(loadFixture(t, "nop_success")))
	})
	c := testClient(srv.URL, newFakeClock())
	ans, err := c.Call(context.Background(), Read, "nop", map[string]any{"domain_name": "example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/nop" || gotQuery != "" {
		t.Fatalf("%s %s ?%s", gotMethod, gotPath, gotQuery)
	}
	if form.Get("username") != "user" || form.Get("password") != "s3cr3t-pass" || form.Get("input_format") != "json" {
		t.Fatalf("form %v", form)
	}
	data := form.Get("input_data")
	if strings.Contains(data, "username") || strings.Contains(data, "password") || !strings.Contains(data, "example.test") {
		t.Fatalf("input_data %s", data)
	}
	if !strings.Contains(string(ans), "user_id") {
		t.Fatalf("answer %s", ans)
	}
}

func TestCallAPIError(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"error","error_code":"ACCESS_DENIED_FROM_IP","error_text":"denied","error_params":{}}`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "ACCESS_DENIED_FROM_IP" {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestReadRetriesOnHTML(t *testing.T) {
	ok := ""
	srv, n := newServer(t, func(i int, w http.ResponseWriter, r *http.Request) {
		if i < 3 {
			w.Write([]byte("<html>"))
			return
		}
		w.Write([]byte(ok))
	})
	ok = loadFixture(t, "nop_success")
	clk := newFakeClock()
	c := testClient(srv.URL, clk)
	if _, err := c.Call(context.Background(), Read, "nop", nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatalf("requests %d", n.Load())
	}
	got := clk.recorded()
	if len(got) != 2 || got[0] != time.Second || got[1] != 2*time.Second {
		t.Fatalf("sleeps %v", got)
	}
}

func TestReadMaxAttempts(t *testing.T) {
	srv, n := newServer(t, body("<html>"))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 3 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestRead5xxWithSuccessBody(t *testing.T) {
	srv, n := newServer(t, func(i int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		w.Write([]byte(`{"result":"success"}`))
	})
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 3 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestReadRetriesServiceUnavailable(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"error","error_code":"SERVICE_UNAVAILABLE","error_text":"x"}`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var ae *APIError
	if !errors.As(err, &ae) || n.Load() != 3 {
		t.Fatalf("err %v requests %d", err, n.Load())
	}
}

func TestMutateNoRetry(t *testing.T) {
	srv, n := newServer(t, body(`{"result":`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Mutate, "zone/add_txt", nil)
	var ue *UnknownOutcomeError
	if !errors.As(err, &ue) || !ue.UnknownOutcome() || ue.Method != "zone/add_txt" {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestMutateAPIErrorIsDefinite(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"error","error_code":"DOMAIN_NOT_FOUND","error_text":"x"}`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Mutate, "zone/add_txt", nil)
	var ae *APIError
	if !errors.As(err, &ae) || n.Load() != 1 {
		t.Fatalf("err %v", err)
	}
}

func TestAuthErrorNotRetried(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"error","error_code":"PASSWORD_AUTH_FAILED","error_text":"bad"}`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var ae *APIError
	if !errors.As(err, &ae) || n.Load() != 1 {
		t.Fatalf("err %v requests %d", err, n.Load())
	}
}

func TestRedirectRefused(t *testing.T) {
	second, n2 := newServer(t, body(`{"result":"success"}`))
	first, _ := newServer(t, func(i int, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/nop", http.StatusFound)
	})
	c := testClient(first.URL, newFakeClock(), WithHTTPClient(&http.Client{}))
	if _, err := c.Call(context.Background(), Read, "nop", nil); err == nil {
		t.Fatal("expected error")
	}
	if n2.Load() != 0 {
		t.Fatalf("second server got %d", n2.Load())
	}
}

func TestPasswordRedacted(t *testing.T) {
	srv, _ := newServer(t, body(`{"result":"error","error_code":"SOME","error_text":"bad s3cr3t-pass here","error_params":{"p":"s3cr3t-pass"}}`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-pass") || strings.Contains(string(ae.Params), "s3cr3t-pass") || strings.Contains(ae.Text, "s3cr3t-pass") {
		t.Fatalf("leak: %v %s", err, ae.Params)
	}
	if !strings.Contains(ae.Text, "***") {
		t.Fatalf("text %q", ae.Text)
	}
}

func TestCredentialsString(t *testing.T) {
	s := Credentials{Username: "u", Password: "p"}.String()
	if s != "username=u password=***" {
		t.Fatal(s)
	}
}

func TestSharedLimiterAcrossClients(t *testing.T) {
	srv, _ := newServer(t, body(`{"result":"success","answer":{}}`))
	clk := newFakeClock()
	lim := NewLimiter(3*time.Second, clk)
	cr := Credentials{Username: "u", Password: "p"}
	a := New(cr, lim, WithBaseURL(srv.URL), WithClock(clk))
	b := New(cr, lim, WithBaseURL(srv.URL), WithClock(clk))
	for i := 0; i < 2; i++ {
		if _, err := a.Call(context.Background(), Read, "nop", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Call(context.Background(), Read, "nop", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := clk.total(); got != 9*time.Second {
		t.Fatalf("total %v", got)
	}
}

func TestLimiterCancel(t *testing.T) {
	srv, n := newServer(t, body(`{"result":"success"}`))
	clk := &blockingClock{now: time.Unix(1000, 0)}
	lim := NewLimiter(3*time.Second, clk)
	if err := lim.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := New(Credentials{Username: "u", Password: "p"}, lim, WithBaseURL(srv.URL), WithClock(clk))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.Call(ctx, Read, "nop", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 0 {
		t.Fatalf("requests %d", n.Load())
	}
}

func TestParamsRedactedAnyEscaping(t *testing.T) {
	payload := `{"result":"error","error_code":"X","error_text":"t","error_params":{"a\/b-пароль":"v","list":["x a\/b-пароль y",{"n":"a/b-пароль"}],"num":5}}`
	srv, _ := newServer(t, body(payload))
	pw := "a/b-пароль"
	c := New(Credentials{Username: "u", Password: pw}, NewLimiter(0, newFakeClock()), WithBaseURL(srv.URL), WithClock(newFakeClock()))
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err %v", err)
	}
	forms := []string{pw, "a/b", `a\/b`, "пароль", `п`, `п`}
	for _, f := range forms {
		if strings.Contains(string(ae.Params), f) || strings.Contains(err.Error(), f) {
			t.Fatalf("leak of %q: %s", f, ae.Params)
		}
	}
	var decoded any
	if err := json.Unmarshal(ae.Params, &decoded); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if strings.Contains(x, pw) {
				t.Fatalf("decoded leak %q", x)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				walk(k)
				walk(e)
			}
		}
	}
	walk(decoded)
	if !strings.Contains(string(ae.Params), "***") {
		t.Fatalf("params %s", ae.Params)
	}
}

func TestParamsInvalidJSONRedacted(t *testing.T) {
	srv, _ := newServer(t, body(`{"result":"error","error_code":"X","error_params":"s3cr3t-pass"}`))
	c := testClient(srv.URL, newFakeClock())
	_, err := c.Call(context.Background(), Read, "nop", nil)
	var ae *APIError
	if !errors.As(err, &ae) || strings.Contains(string(ae.Params), "s3cr3t-pass") {
		t.Fatalf("err %v", err)
	}
}
