package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/lavr/regru-cli/internal/config"
	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/regruzone"
	"github.com/lavr/regru-cli/internal/zone"
)

const twoAccounts = `version: 1
default_account: a
accounts:
  a:
    username: user
    password: env:A_PW
  b:
    username: userb
    password: env:B_PW
`

type probeResult struct {
	settings *Settings
	session  *Session
	deadline time.Duration
	err      error
}

func probe(t *testing.T, d *Deps, tgt Target, args ...string) (int, probeResult) {
	t.Helper()
	var res probeResult
	st := &state{env: d.Env, deps: d}
	root := newRoot(st)
	cmd := &cobra.Command{
		Use:  "probe",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := d.LoadSettings(cmd)
			if err != nil {
				res.err = err
				return err
			}
			res.settings = s
			ctx, cancel := d.Context(s)
			defer cancel()
			if dl, ok := ctx.Deadline(); ok {
				res.deadline = time.Until(dl)
			}
			sess, err := d.Open(ctx, s, tgt)
			if err != nil {
				res.err = err
				return err
			}
			res.session = sess
			return nil
		},
	}
	cmd.Flags().Int("max-deletes", 10, "")
	root.AddCommand(cmd)
	return run(st, root, append([]string{"probe"}, args...)), res
}

func TestAccountConflictWithPinned(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw", "B_PW", "pwb")
	code, res := probe(t, d, Target{Domain: "example.test", Pinned: "b"}, "--account", "a", "--output", "json")
	if code != 2 {
		t.Fatalf("exit %d, err %v", code, res.err)
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].Code != "account_conflict" {
		t.Fatalf("%+v", e)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestPinnedMatchingExplicitOK(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw", "B_PW", "pwb")
	code, res := probe(t, d, Target{Domain: "example.test", Pinned: "b"}, "--account", "b")
	if code != 0 || res.session.Account.Name != "b" {
		t.Fatalf("exit %d, %+v", code, res)
	}
}

func TestPinnedWinsOverDefault(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw", "B_PW", "pwb")
	code, res := probe(t, d, Target{Domain: "example.test", Pinned: "b"})
	if code != 0 {
		t.Fatalf("exit %d: %v", code, res.err)
	}
	if res.session.Account.Name != "b" || res.session.Account.Username != "userb" || res.session.Account.Password != "pwb" {
		t.Fatalf("%+v", res.session.Account)
	}
}

func TestMaxDeletesFlagChanged(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts+"defaults:\n  max_deletes: 0\n")
	withEnv(d, "A_PW", "pw")
	code, res := probe(t, d, Target{}, "--max-deletes", "5")
	if code != 0 || res.settings.MaxDeletes != 5 {
		t.Fatalf("exit %d, %+v", code, res.settings)
	}
	code, res = probe(t, d, Target{})
	if code != 0 || res.settings.MaxDeletes != 0 {
		t.Fatalf("exit %d, %+v", code, res.settings)
	}
}

func TestMaxDeletesDefault(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	code, res := probe(t, d, Target{})
	if code != 0 || res.settings.MaxDeletes != 10 {
		t.Fatalf("exit %d, %+v", code, res.settings)
	}
	if res.settings.Timeout != 5*time.Minute || res.settings.RequestTimeout != 30*time.Second {
		t.Fatalf("%+v", res.settings)
	}
	if res.session.Account.Name != "env" || res.session.Account.Username != "user" {
		t.Fatalf("%+v", res.session.Account)
	}
}

func TestTimeoutFromConfig(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts+"defaults:\n  timeout: 1s\n")
	withEnv(d, "A_PW", "pw")
	code, res := probe(t, d, Target{})
	if code != 0 || res.settings.Timeout != time.Second {
		t.Fatalf("exit %d, %+v", code, res.settings)
	}
	if res.deadline <= 0 || res.deadline > time.Second {
		t.Fatalf("deadline in %v", res.deadline)
	}
	code, res = probe(t, d, Target{}, "--timeout", "3s")
	if code != 0 || res.settings.Timeout != 3*time.Second || res.deadline <= time.Second {
		t.Fatalf("exit %d, %+v %v", code, res.settings, res.deadline)
	}
}

func TestInvalidTimeoutFlag(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	code, _ := probe(t, d, Target{}, "--timeout", "-1s")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestAutoDiscoveryBoundedByTimeout(t *testing.T) {
	api := newFakeAPI(t)
	api.set(func(f *fakeAPI) { f.hangOn = "service/get_list" })
	d := newTestDeps(t, api)
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw")
	var gotDomain string
	var gotFresh bool
	d.Discover = func(ctx context.Context, d *Deps, domain string, fresh bool) (string, error) {
		gotDomain, gotFresh = domain, fresh
		acc, err := d.active.Config.Resolve("a", d.Env.LookupEnv, nil)
		if err != nil {
			return "", err
		}
		_, err = d.NewClient(acc, d.Limiter, time.Minute).ListDomains(ctx)
		return "", err
	}
	start := time.Now()
	code, res := probe(t, d, Target{Domain: "example.test", Mutating: true}, "--account", "auto", "--timeout", "50ms", "--output", "json")
	if code != 1 {
		t.Fatalf("exit %d: %v", code, res.err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].Code != "timeout" {
		t.Fatalf("%+v", e)
	}
	if gotDomain != "example.test" || !gotFresh {
		t.Fatalf("discover got %q %v", gotDomain, gotFresh)
	}
}

func TestAutoWithoutDomainNotAllowed(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	called := false
	d.Discover = func(context.Context, *Deps, string, bool) (string, error) {
		called = true
		return "a", nil
	}
	code, res := probe(t, d, Target{}, "--account", "auto")
	if code != 2 || ToCoded(res.err).Item.Code != "auto_not_allowed" || called {
		t.Fatalf("exit %d, %v, called %v", code, res.err, called)
	}
}

func TestAutoResolvesDiscoveredAccount(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	withEnv(d, "B_PW", "pwb")
	d.Discover = func(context.Context, *Deps, string, bool) (string, error) { return "b", nil }
	code, res := probe(t, d, Target{Domain: "example.test"}, "--account", "auto")
	if code != 0 || res.session.Account.Name != "b" {
		t.Fatalf("exit %d, %v", code, res.err)
	}
}

func TestDiscoverStubNotImplemented(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	code, res := probe(t, d, Target{Domain: "example.test"}, "--account", "auto")
	if code != 2 || ToCoded(res.err).Item.Code != "auto_not_implemented" {
		t.Fatalf("exit %d, %v", code, res.err)
	}
}

func TestRequestTimeoutPassed(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts+"defaults:\n  request_timeout: 7s\n")
	withEnv(d, "A_PW", "pw")
	var got time.Duration
	inner := d.NewClient
	d.NewClient = func(a config.Account, l *regruapi.Limiter, rt time.Duration) *regruapi.Client {
		got = rt
		return inner(a, l, rt)
	}
	code, res := probe(t, d, Target{})
	if code != 0 || got != 7*time.Second || res.settings.RequestTimeout != 7*time.Second {
		t.Fatalf("exit %d, got %v, %v", code, got, res.err)
	}
}

func TestOneLimiterPerProcess(t *testing.T) {
	if NewDeps(Env{LookupEnv: func(string) (string, bool) { return "", false }}).Limiter == nil {
		t.Fatal("NewDeps must create a limiter")
	}
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw", "B_PW", "pwb")
	var lims []*regruapi.Limiter
	inner := d.NewClient
	d.NewClient = func(a config.Account, l *regruapi.Limiter, rt time.Duration) *regruapi.Client {
		lims = append(lims, l)
		return inner(a, l, rt)
	}
	if code, res := probe(t, d, Target{}, "--account", "a"); code != 0 {
		t.Fatalf("exit %d %v", code, res.err)
	}
	if code, res := probe(t, d, Target{}, "--account", "b"); code != 0 {
		t.Fatalf("exit %d %v", code, res.err)
	}
	if len(lims) != 2 || lims[0] != d.Limiter || lims[1] != d.Limiter {
		t.Fatalf("limiters %v, want %p", lims, d.Limiter)
	}
}

func TestLoadSettingsNoNetwork(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw")
	if code, res := probe(t, d, Target{Domain: "example.test", Mutating: true}); code != 0 {
		t.Fatalf("exit %d %v", code, res.err)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestExplicitConfigMissing(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	code, res := probe(t, d, Target{}, "--config", d.Paths.Config+"/nope.yaml")
	if code != 2 || ToCoded(res.err).Item.Code != "config_not_found" {
		t.Fatalf("exit %d %v", code, res.err)
	}
	d = newTestDeps(t, newFakeAPI(t))
	withEnv(d, "REGRU_CONFIG", d.Paths.Config+"/nope.yaml")
	code, res = probe(t, d, Target{})
	if code != 2 || ToCoded(res.err).Item.Code != "config_not_found" {
		t.Fatalf("env: exit %d %v", code, res.err)
	}
}

func TestMissingEnvIsUsage(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, twoAccounts)
	code, res := probe(t, d, Target{})
	if code != 2 || ToCoded(res.err).Item.Code != "missing_env" || api.Requests.Load() != 0 {
		t.Fatalf("exit %d %v", code, res.err)
	}
}

func TestPinnedEnvWithConfigRejected(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	code, res := probe(t, d, Target{Pinned: "env"})
	if code != 2 {
		t.Fatalf("exit %d %v", code, res.err)
	}
}

func TestToCodedTable(t *testing.T) {
	params := json.RawMessage(`{"conflicting_records":[]}`)
	cases := []struct {
		name     string
		err      error
		exit     int
		code     string
		provider string
		domain   string
		details  bool
	}{
		{"coded", &CodedError{Exit: 4, Item: ErrorItem{Code: "drift", Message: "m"}}, 4, "drift", "", "", false},
		{"config", &config.Error{Code: "missing_env", Msg: "m"}, 2, "missing_env", "", "", false},
		{"value", &zone.ValueError{Type: zone.A, Input: "x", Reason: "bad"}, 2, "invalid_value", "", "", false},
		{"name", &zone.NameError{CodeValue: "name_outside_zone", Input: "x", Reason: "r"}, 2, "name_outside_zone", "", "", false},
		{"api", &regruapi.APIError{Code: "PASSWORD_AUTH_FAILED", Text: "t"}, 1, "api_error", "PASSWORD_AUTH_FAILED", "", false},
		{"domain", &regruapi.DomainError{Domain: "example.test", Code: "CONFLICT_CNAME", Text: "t", Params: params}, 1, "domain_error", "CONFLICT_CNAME", "example.test", true},
		{"incomplete", &regruapi.IncompleteReadError{Method: "m", Reason: "r"}, 1, "incomplete_read", "", "", false},
		{"transport", &regruapi.TransportError{Method: "m", Err: errors.New("x")}, 1, "transport_error", "", "", false},
		{"transport_deadline", &regruapi.TransportError{Method: "m", Err: context.DeadlineExceeded}, 1, "transport_error", "", "", false},
		{"unknown", &regruapi.UnknownOutcomeError{Method: "m", Err: context.DeadlineExceeded}, 3, "unknown_outcome", "", "", false},
		{"policy", &plan.PolicyError{Code: "max_deletes_exceeded", Detail: "d"}, 1, "max_deletes_exceeded", "", "", false},
		{"deadline", context.DeadlineExceeded, 1, "timeout", "", "", false},
		{"wrapped_deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), 1, "timeout", "", "", false},
		{"canceled", context.Canceled, 1, "interrupted", "", "", false},
		{"wrapped_config", fmt.Errorf("x: %w", &config.Error{Code: "unknown_account", Msg: "m"}), 2, "unknown_account", "", "", false},
		{"plain", errors.New("boom"), 1, "error", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ToCoded(c.err)
			if got.Exit != c.exit || got.Item.Code != c.code || got.Item.ProviderCode != c.provider || got.Item.Domain != c.domain {
				t.Fatalf("%+v", got)
			}
			if got.Item.Message == "" {
				t.Fatalf("empty message: %+v", got)
			}
			if (got.Item.Details != nil) != c.details {
				t.Fatalf("details %+v", got.Item.Details)
			}
		})
	}
}

func TestCommandGroupsRegistered(t *testing.T) {
	d := newTestDeps(t, nil)
	root := NewRoot(d.Env, d)
	for _, name := range []string{"account", "domain", "dns"} {
		c, _, err := root.Find([]string{name})
		if err != nil || c == nil || c.Name() != name {
			t.Fatalf("group %s: %v", name, err)
		}
	}
}

func TestExecuteNilDeps(t *testing.T) {
	env, out, _ := testEnv(nil)
	if code := Execute([]string{"version", "--output", "json"}, env, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	decodeOne(t, out)
}

func TestCLIRules(t *testing.T) {
	r := cliRules()
	if !r.Caps[zone.TXT].Write || r.Caps[zone.HTTPS].Write {
		t.Fatalf("%+v", r.Caps)
	}
	if !r.Protected(zone.Key{Name: "@", Type: zone.NS}) || r.ValidateWrite == nil {
		t.Fatal("rules")
	}
}

func TestCmdStateAvailable(t *testing.T) {
	d := newTestDeps(t, nil)
	st := &state{env: d.Env, deps: d}
	root := newRoot(st)
	var got *state
	root.AddCommand(&cobra.Command{Use: "x", RunE: func(cmd *cobra.Command, args []string) error {
		got = cmdState(cmd)
		got.exit = got.output.Done(StatusSuccess, map[string]int{"n": 1}, nil, nil)
		return nil
	}})
	if code := run(st, root, []string{"x", "--output", "json"}); code != 0 || got != st {
		t.Fatalf("exit %d", code)
	}
	decodeOne(t, stdoutOf(d))
}

func fakeBackend(t *testing.T, api *fakeAPI) *regruzone.Backend {
	d := newTestDeps(t, api)
	c := d.NewClient(config.Account{Name: "env", Username: "user", Password: "pw"}, d.Limiter, time.Minute)
	return regruzone.NewBackend(c, "example.test")
}

func TestFakeAPIRoundTrip(t *testing.T) {
	api := newFakeAPI(t)
	b := fakeBackend(t, api)
	ctx := context.Background()
	if id, err := b.Identity(ctx); err != nil || id != "1000" {
		t.Fatalf("%q %v", id, err)
	}
	add := []zone.Record{
		{Name: "a", Type: zone.A, Value: "192.0.2.1"},
		{Name: "a", Type: zone.A, Value: "192.0.2.2"},
		{Name: "v6", Type: zone.AAAA, Value: "2001:db8::1"},
		{Name: "c", Type: zone.CNAME, Value: "t.example.net."},
		{Name: "mx", Type: zone.MX, Value: "10 mx.example.net."},
		{Name: "mx", Type: zone.MX, Value: "20 mx.example.net."},
		{Name: "txt", Type: zone.TXT, Value: "hello world"},
		{Name: "sub", Type: zone.NS, Value: "ns1.example.net."},
		{Name: "_sip._tcp", Type: zone.SRV, Value: "10 5 5060 sip.example.net."},
		{Name: "caa", Type: zone.CAA, Value: "0 issue a.example.net"},
	}
	for _, r := range add {
		if err := b.Add(ctx, r); err != nil {
			t.Fatalf("add %v: %v", r, err)
		}
	}
	s, sid, err := b.Snapshot(ctx)
	if err != nil || sid != "1000001" {
		t.Fatalf("%q %v", sid, err)
	}
	have := map[zone.Record]bool{}
	for _, r := range s.Records {
		have[r] = true
	}
	for _, r := range add {
		if !have[r] {
			t.Fatalf("missing %v in %v (raw %v)", r, s.Records, s.Raw)
		}
	}
	if len(s.Raw) != 0 {
		t.Fatalf("raw %v", s.Raw)
	}
	for _, r := range add {
		if err := b.Remove(ctx, r); err != nil {
			t.Fatalf("remove %v: %v", r, err)
		}
	}
	if got := api.records("example.test"); len(got) != len(baselineRRs()) {
		t.Fatalf("%v", got)
	}
	if n := api.Mutations.Load(); n != int64(2*len(add)) {
		t.Fatalf("mutations %d", n)
	}
}

func TestFakeAPIShapes(t *testing.T) {
	api := newFakeAPI(t)
	b := fakeBackend(t, api)
	ctx := context.Background()
	b.Add(ctx, zone.Record{Name: "v6", Type: zone.AAAA, Value: "2001:db8::1"})
	b.Add(ctx, zone.Record{Name: "_s._tcp", Type: zone.SRV, Value: "10 5 5060 sip.example.net."})
	b.Add(ctx, zone.Record{Name: "caa", Type: zone.CAA, Value: "0 issue a.example.net"})
	byType := map[string]fakeRR{}
	for _, r := range api.records("example.test") {
		byType[r.Rectype] = r
	}
	if byType["AAAA"].Prio != nil {
		t.Fatalf("aaaa prio %v", *byType["AAAA"].Prio)
	}
	if srv := byType["SRV"]; srv.Content != "5 5060 sip.example.net." || *srv.Prio != 10 || *srv.Weight != 5 || *srv.Port != 5060 {
		t.Fatalf("srv %+v", srv)
	}
	if c := byType["CAA"].Content; c != `0 issue "a.example.net"` {
		t.Fatalf("caa %q", c)
	}
}

func TestFakeAPIModes(t *testing.T) {
	api := newFakeAPI(t)
	b := fakeBackend(t, api)
	ctx := context.Background()

	err := b.Add(ctx, zone.Record{Name: "www", Type: zone.CNAME, Value: "t.example.net."})
	var de *regruapi.DomainError
	if !errors.As(err, &de) || de.Code != "CONFLICT_CNAME" || !strings.Contains(string(de.Params), "conflicting_records") {
		t.Fatalf("%v", err)
	}

	api.set(func(f *fakeAPI) { f.dropOnMutate = true })
	err = b.Add(ctx, zone.Record{Name: "n", Type: zone.A, Value: "192.0.2.9"})
	var ue *regruapi.UnknownOutcomeError
	if !errors.As(err, &ue) {
		t.Fatalf("%v", err)
	}
	found := false
	for _, r := range api.records("example.test") {
		if r.Subname == "n" {
			found = true
		}
	}
	if !found {
		t.Fatal("dropOnMutate must apply the mutation")
	}

	reads := 0
	api.set(func(f *fakeAPI) {
		f.failNextRead = true
		f.onRead = func() { reads++ }
	})
	_, _, err = b.Snapshot(ctx)
	var ie *regruapi.IncompleteReadError
	if !errors.As(err, &ie) {
		t.Fatalf("%v", err)
	}
	if _, _, err = b.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("onRead %d", reads)
	}

	api.setNSS("example.test", "ns1.example.net")
	_, _, err = b.Snapshot(ctx)
	if !errors.As(err, &de) || de.Code != "DOMAIN_IS_NOT_USE_REGRU_NSS" {
		t.Fatalf("%v", err)
	}

	d := newTestDeps(t, api)
	bad := d.NewClient(config.Account{Name: "x", Username: "user", Password: "wrong"}, d.Limiter, time.Minute)
	_, _, err = bad.Nop(ctx)
	var ae *regruapi.APIError
	if !errors.As(err, &ae) || ae.Code != "PASSWORD_AUTH_FAILED" {
		t.Fatalf("%v", err)
	}
	if p, err := bad.IsPartner(ctx); err == nil || p {
		t.Fatalf("%v %v", p, err)
	}
	c := d.NewClient(config.Account{Name: "x", Username: "user", Password: "pw"}, d.Limiter, time.Minute)
	if p, err := c.IsPartner(ctx); err != nil || p {
		t.Fatalf("%v %v", p, err)
	}
	ds, err := c.ListDomains(ctx)
	if err != nil || len(ds) != 1 || ds[0].Domain != "example.test" || ds[0].ServiceID != "1000001" {
		t.Fatalf("%v %v", ds, err)
	}
	api.setNSS("example.test", "ns1.reg.ru", "ns2.reg.ru")
	nss, err := c.GetNSS(ctx, []string{"example.test", "example.net"})
	if err != nil || len(nss) != 2 || nss[0].NSS[1] != "ns2.reg.ru" || nss[1].Err == nil {
		t.Fatalf("%+v %v", nss, err)
	}
}

func TestAutoWithPinnedUsesPinned(t *testing.T) {
	d := newTestDeps(t, newFakeAPI(t))
	writeConfig(t, d, twoAccounts)
	withEnv(d, "A_PW", "pw", "B_PW", "pwb")
	called := false
	d.Discover = func(context.Context, *Deps, string, bool) (string, error) {
		called = true
		return "a", nil
	}
	code, res := probe(t, d, Target{Domain: "example.test", Pinned: "b", Mutating: true}, "--account", "auto")
	if code != 0 || res.session.Account.Name != "b" || called {
		t.Fatalf("exit %d, %v, called %v", code, res.err, called)
	}
}

func fakeRawAdd(t *testing.T, api *fakeAPI, fn string, params map[string]string) error {
	t.Helper()
	d := newTestDeps(t, api)
	c := d.NewClient(config.Account{Name: "env", Username: "user", Password: "pw"}, d.Limiter, time.Minute)
	return c.ZoneMutate(context.Background(), fn, "example.test", params)
}

func TestFakeTXTInvalidCharacters(t *testing.T) {
	api := newFakeAPI(t)
	for _, text := range []string{"проверка regru", "tab\there", "del\x7f"} {
		err := fakeRawAdd(t, api, "add_txt", map[string]string{"subdomain": "txt7.probe", "text": text})
		var de *regruapi.DomainError
		if !errors.As(err, &de) || de.Code != "TEXT_CONTAINS_INVALID_CHARACTERS" || de.Domain != "example.test" || string(de.Params) != "{}" {
			t.Fatalf("%q: %v", text, err)
		}
	}
	if err := fakeRawAdd(t, api, "add_txt", map[string]string{"subdomain": "txt1.probe", "text": "v=spf1 ~all"}); err != nil {
		t.Fatal(err)
	}
	if n := len(api.records("example.test")); n != len(baselineRRs())+1 {
		t.Fatalf("%d records", n)
	}
}

func TestFakeFailNextMutate(t *testing.T) {
	api := newFakeAPI(t)
	api.set(func(f *fakeAPI) { f.failNextMutate = "DOMAIN_BAD_STATE" })
	err := fakeRawAdd(t, api, "add_alias", map[string]string{"subdomain": "x", "ipaddr": "192.0.2.5"})
	var de *regruapi.DomainError
	if !errors.As(err, &de) || de.Code != "DOMAIN_BAD_STATE" {
		t.Fatalf("%v", err)
	}
	if n := len(api.records("example.test")); n != len(baselineRRs()) {
		t.Fatalf("applied: %d records", n)
	}
	if err := fakeRawAdd(t, api, "add_alias", map[string]string{"subdomain": "x", "ipaddr": "192.0.2.5"}); err != nil {
		t.Fatalf("not cleared: %v", err)
	}
	if n := len(api.records("example.test")); n != len(baselineRRs())+1 {
		t.Fatalf("%d records", n)
	}
}

func TestFakeSubnameExactEverywhere(t *testing.T) {
	api := newFakeAPI(t)
	if err := fakeRawAdd(t, api, "add_alias", map[string]string{"subdomain": "Mixed", "ipaddr": "192.0.2.5"}); err != nil {
		t.Fatal(err)
	}
	if err := fakeRawAdd(t, api, "add_alias", map[string]string{"subdomain": "mixed", "ipaddr": "192.0.2.5"}); err != nil {
		t.Fatal(err)
	}
	if err := fakeRawAdd(t, api, "add_cname", map[string]string{"subdomain": "MIXED", "canonical_name": "t.example.net."}); err != nil {
		t.Fatalf("cname conflict must compare exactly: %v", err)
	}
	if n := len(api.records("example.test")); n != len(baselineRRs())+3 {
		t.Fatalf("%d records", n)
	}
	rm := map[string]string{"subdomain": "mixed", "record_type": "A", "priority": "0", "content": "192.0.2.5"}
	if err := fakeRawAdd(t, api, "remove_record", rm); err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	for _, r := range api.records("example.test") {
		left[r.Subname] = true
	}
	if left["mixed"] || !left["Mixed"] || !left["MIXED"] {
		t.Fatalf("%v", left)
	}
}
