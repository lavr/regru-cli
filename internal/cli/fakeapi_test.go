package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lavr/regru-cli/internal/config"
	"github.com/lavr/regru-cli/internal/regruapi"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.Advance(d)
	return ctx.Err()
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fakeRR struct {
	Subname string
	Rectype string
	Content string
	Prio    *int
	Weight  *int
	Port    *int
}

func intp(i int) *int { return &i }

func (r fakeRR) toJSON() map[string]any {
	m := map[string]any{
		"subname": r.Subname,
		"rectype": r.Rectype,
		"content": r.Content,
		"state":   "A",
		"prio":    nil,
	}
	if r.Prio != nil {
		m["prio"] = *r.Prio
	}
	if r.Weight != nil {
		m["weight"] = *r.Weight
	}
	if r.Port != nil {
		m["port"] = *r.Port
	}
	return m
}

func (r fakeRR) same(o fakeRR) bool {
	eq := func(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	return r.Subname == o.Subname && r.Rectype == o.Rectype && r.Content == o.Content &&
		eq(r.Prio, o.Prio) && eq(r.Weight, o.Weight) && eq(r.Port, o.Port)
}

func baselineRRs() []fakeRR {
	return []fakeRR{
		{Subname: "@", Rectype: "A", Content: "192.0.2.200", Prio: intp(0)},
		{Subname: "@", Rectype: "NS", Content: "ns1.reg.ru.", Prio: intp(0)},
		{Subname: "@", Rectype: "NS", Content: "ns2.reg.ru.", Prio: intp(1)},
		{Subname: "www", Rectype: "A", Content: "192.0.2.200", Prio: intp(0)},
	}
}

type fakeZone struct {
	ServiceID string
	NSS       []string
	RRs       []fakeRR
}

type fakeUser struct {
	Password string
	Login    string
	UserID   int
	Partner  bool
	Domains  []string
}

type fakeCall struct {
	Method   string
	Username string
	Input    map[string]any
}

type fakeAPI struct {
	t       *testing.T
	URL     string
	clock   *fakeClock
	closing chan struct{}

	Requests  atomic.Int64
	Mutations atomic.Int64

	mu             sync.Mutex
	users          map[string]*fakeUser
	zones          map[string]*fakeZone
	calls          []fakeCall
	dropOnMutate   bool
	failNextRead   bool
	failNextMutate string
	onRead         func()
	hangOn         string
}

var fakeMutations = map[string]bool{
	"zone/add_alias": true, "zone/add_aaaa": true, "zone/add_cname": true, "zone/add_mx": true,
	"zone/add_txt": true, "zone/add_ns": true, "zone/add_srv": true, "zone/add_caa": true,
	"zone/remove_record": true,
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		t:       t,
		clock:   &fakeClock{now: time.Unix(1_800_000_000, 0)},
		closing: make(chan struct{}),
		users:   map[string]*fakeUser{},
		zones:   map[string]*fakeZone{},
	}
	f.addUser("user", "pw", 1000, "example.test")
	f.addZone("example.test", "1000001", baselineRRs()...)
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = srv.URL
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(f.closing) })
	return f
}

func (f *fakeAPI) set(fn func(f *fakeAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAPI) addUser(username, password string, userID int, domains ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[username] = &fakeUser{Password: password, Login: username, UserID: userID, Domains: append([]string(nil), domains...)}
}

func (f *fakeAPI) setPartner(username string, partner bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[username].Partner = partner
}

func (f *fakeAPI) addZone(domain, serviceID string, rrs ...fakeRR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones[domain] = &fakeZone{ServiceID: serviceID, NSS: []string{"ns1.reg.ru", "ns2.reg.ru"}, RRs: append([]fakeRR(nil), rrs...)}
}

func (f *fakeAPI) setNSS(domain string, nss ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones[domain].NSS = append([]string(nil), nss...)
}

func (f *fakeAPI) records(domain string) []fakeRR {
	f.mu.Lock()
	defer f.mu.Unlock()
	z, ok := f.zones[domain]
	if !ok {
		return nil
	}
	return append([]fakeRR(nil), z.RRs...)
}

func (f *fakeAPI) recorded() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...)
}

func fakeSuccess(answer any) []byte {
	b, _ := json.Marshal(map[string]any{"answer": answer, "charset": "utf-8", "messagestore": nil, "result": "success"})
	return b
}

func fakeError(code, text string) []byte {
	b, _ := json.Marshal(map[string]any{"error_code": code, "error_text": text, "charset": "utf-8", "messagestore": nil, "result": "error"})
	return b
}

func fakeDomainError(dname, serviceID, code, text string, params any) map[string]any {
	m := map[string]any{"dname": dname, "error_code": code, "error_text": text, "result": "error"}
	if serviceID != "" {
		m["service_id"] = serviceID
	}
	if params != nil {
		m["error_params"] = params
	}
	return m
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.Requests.Add(1)
	method := strings.Trim(r.URL.Path, "/")
	if r.Method != http.MethodPost {
		f.t.Errorf("fakeAPI: %s %s: expected POST", r.Method, method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("fakeAPI: %s: bad form: %v", method, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mutating := fakeMutations[method]
	if mutating {
		f.Mutations.Add(1)
	}
	var input map[string]any
	if r.PostForm.Get("input_format") != "json" {
		f.reply(w, fakeError("INVALID_INPUT_FORMAT", "input_format must be json"))
		return
	}
	dec := json.NewDecoder(strings.NewReader(r.PostForm.Get("input_data")))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		f.reply(w, fakeError("INPUT_DATA_PARSE_ERROR", "input_data is not json"))
		return
	}
	if _, ok := input["username"]; ok {
		f.t.Errorf("fakeAPI: %s: username inside input_data", method)
	}
	if _, ok := input["password"]; ok {
		f.t.Errorf("fakeAPI: %s: password inside input_data", method)
	}
	username := r.PostForm.Get("username")

	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Method: method, Username: username, Input: input})
	hang := f.hangOn != "" && f.hangOn == method
	onRead := f.onRead
	f.mu.Unlock()

	if hang {
		select {
		case <-r.Context().Done():
		case <-f.closing:
		}
		return
	}
	if !mutating && onRead != nil {
		onRead()
	}

	f.mu.Lock()
	body := f.handle(method, username, r.PostForm.Get("password"), input)
	drop := mutating && f.dropOnMutate
	f.mu.Unlock()

	if drop {
		hj, ok := w.(http.Hijacker)
		if !ok {
			f.t.Errorf("fakeAPI: cannot hijack connection")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			if err := conn.Close(); err != nil {
				f.t.Errorf("fakeAPI: close hijacked connection: %v", err)
			}
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	f.reply(w, body)
}

func (f *fakeAPI) reply(w http.ResponseWriter, b []byte) {
	if _, err := w.Write(b); err != nil {
		f.t.Errorf("fakeAPI: write reply: %v", err)
	}
}

func inStr(in map[string]any, k string) string {
	v, ok := in[k]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func inDomains(in map[string]any) []string {
	raw, _ := in["domains"].([]any)
	out := make([]string, 0, len(raw))
	for _, d := range raw {
		m, _ := d.(map[string]any)
		out = append(out, inStr(m, "dname"))
	}
	return out
}

func (u *fakeUser) owns(domain string) bool {
	for _, d := range u.Domains {
		if d == domain {
			return true
		}
	}
	return false
}

func (f *fakeAPI) handle(method, username, password string, in map[string]any) []byte {
	u, ok := f.users[username]
	if !ok || u.Password != password || password == "" {
		return fakeError("PASSWORD_AUTH_FAILED", "Username/password Incorrect")
	}
	switch method {
	case "nop":
		return fakeSuccess(map[string]any{"login": u.Login, "user_id": u.UserID})
	case "reseller_nop":
		if !u.Partner {
			return fakeError("RESELLER_AUTH_FAILED", "Only resellers can use this function")
		}
		return fakeSuccess(map[string]any{"login": u.Login, "user_id": u.UserID})
	case "service/get_list":
		services := []map[string]any{}
		for _, d := range u.Domains {
			sid := ""
			if z, ok := f.zones[d]; ok {
				sid = z.ServiceID
			}
			services = append(services, map[string]any{"dname": d, "service_id": sid, "servtype": "domain", "state": "A"})
		}
		return fakeSuccess(map[string]any{"services": services})
	case "domain/get_nss":
		if f.takeFailRead() {
			return fakeSuccess(map[string]any{"domains": []any{}})
		}
		items := []map[string]any{}
		for _, d := range inDomains(in) {
			z, ok := f.zones[d]
			if !ok || !u.owns(d) {
				items = append(items, fakeDomainError(d, "", "DOMAIN_NOT_FOUND", "Domain not found", nil))
				continue
			}
			nss := []map[string]string{}
			for _, n := range z.NSS {
				nss = append(nss, map[string]string{"ns": n})
			}
			items = append(items, map[string]any{"dname": d, "nss": nss, "service_id": z.ServiceID})
		}
		return fakeSuccess(map[string]any{"domains": items})
	case "zone/get_resource_records":
		if f.takeFailRead() {
			return fakeSuccess(map[string]any{"domains": []any{}})
		}
		items := []map[string]any{}
		for _, d := range inDomains(in) {
			z, ok := f.zones[d]
			switch {
			case !ok || !u.owns(d):
				items = append(items, fakeDomainError(d, "", "DOMAIN_NOT_FOUND", "Domain not found", nil))
			case !z.regruNS():
				items = append(items, fakeDomainError(d, z.ServiceID, "DOMAIN_IS_NOT_USE_REGRU_NSS", "Domain does not use reg.ru nameservers", nil))
			default:
				rrs := []map[string]any{}
				for _, rr := range z.RRs {
					rrs = append(rrs, rr.toJSON())
				}
				items = append(items, map[string]any{
					"dname": d, "result": "success", "rrs": rrs, "service_id": z.ServiceID, "servtype": "domain",
					"soa": map[string]string{"minimum_ttl": "3h", "ttl": "1d"},
				})
			}
		}
		return fakeSuccess(map[string]any{"domains": items})
	}
	if fakeMutations[method] {
		ds := inDomains(in)
		if len(ds) != 1 {
			return fakeError("INVALID_DOMAINS", "exactly one domain expected")
		}
		d := ds[0]
		z, ok := f.zones[d]
		if code := f.failNextMutate; code != "" {
			f.failNextMutate = ""
			sid := ""
			if ok {
				sid = z.ServiceID
			}
			return fakeSuccess(map[string]any{"domains": []any{fakeDomainError(d, sid, code, "Injected failure "+code, map[string]any{})}})
		}
		if !ok || !u.owns(d) {
			return fakeSuccess(map[string]any{"domains": []any{fakeDomainError(d, "", "DOMAIN_NOT_FOUND", "Domain not found", nil)}})
		}
		item := z.mutate(d, strings.TrimPrefix(method, "zone/"), in)
		return fakeSuccess(map[string]any{"domains": []any{item}})
	}
	return fakeError("NO_SUCH_COMMAND", "No such command: "+method)
}

func (f *fakeAPI) takeFailRead() bool {
	if f.failNextRead {
		f.failNextRead = false
		return true
	}
	return false
}

func (z *fakeZone) regruNS() bool {
	return len(z.NSS) == 2 && z.NSS[0] == "ns1.reg.ru" && z.NSS[1] == "ns2.reg.ru"
}

func caaQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func atoiP(s string) (*int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, false
	}
	return &n, true
}

func (z *fakeZone) mutate(dname, fn string, in map[string]any) map[string]any {
	ok := map[string]any{"dname": dname, "result": "success", "service_id": z.ServiceID}
	bad := func(code, text string) map[string]any {
		return fakeDomainError(dname, z.ServiceID, code, text, map[string]any{})
	}
	if fn == "remove_record" {
		sub, typ, prio, content := inStr(in, "subdomain"), inStr(in, "record_type"), inStr(in, "priority"), inStr(in, "content")
		if sub == "" || typ == "" {
			return bad("PARAMETER_MISSING", "subdomain and record_type are required")
		}
		kept := z.RRs[:0:0]
		for _, rr := range z.RRs {
			match := rr.Subname == sub && rr.Rectype == typ && rr.Content == content
			if match {
				if rr.Prio != nil {
					match = prio == strconv.Itoa(*rr.Prio)
				} else {
					match = prio == "" || prio == "0"
				}
			}
			if !match {
				kept = append(kept, rr)
			}
		}
		z.RRs = kept
		return ok
	}
	sub := inStr(in, "subdomain")
	var rr fakeRR
	switch fn {
	case "add_alias":
		rr = fakeRR{Subname: sub, Rectype: "A", Content: inStr(in, "ipaddr"), Prio: intp(0)}
	case "add_aaaa":
		rr = fakeRR{Subname: sub, Rectype: "AAAA", Content: inStr(in, "ipaddr")}
	case "add_cname":
		rr = fakeRR{Subname: sub, Rectype: "CNAME", Content: inStr(in, "canonical_name"), Prio: intp(0)}
	case "add_mx":
		p, good := atoiP(inStr(in, "priority"))
		if !good {
			return bad("INVALID_PRIORITY", "priority must be a number")
		}
		rr = fakeRR{Subname: sub, Rectype: "MX", Content: inStr(in, "mail_server"), Prio: p}
	case "add_txt":
		text := inStr(in, "text")
		for i := 0; i < len(text); i++ {
			if text[i] < 0x20 || text[i] > 0x7E {
				return bad("TEXT_CONTAINS_INVALID_CHARACTERS", "Text contains invalid characters")
			}
		}
		rr = fakeRR{Subname: sub, Rectype: "TXT", Content: inStr(in, "text"), Prio: intp(0)}
	case "add_ns":
		rr = fakeRR{Subname: sub, Rectype: "NS", Content: inStr(in, "dns_server"), Prio: intp(0)}
	case "add_srv":
		p, g1 := atoiP(inStr(in, "priority"))
		w, g2 := atoiP(inStr(in, "weight"))
		port, g3 := atoiP(inStr(in, "port"))
		target := inStr(in, "target")
		if !g1 || !g2 || !g3 || target == "" {
			return bad("INVALID_SRV", "priority, weight, port and target are required")
		}
		rr = fakeRR{Subname: inStr(in, "service"), Rectype: "SRV", Content: fmt.Sprintf("%d %d %s", *w, *port, target), Prio: p, Weight: w, Port: port}
	case "add_caa":
		flags, tag, value := inStr(in, "flags"), inStr(in, "tag"), inStr(in, "value")
		if flags == "" || tag == "" || value == "" {
			return bad("PARAMETER_MISSING", "flags, tag and value are required")
		}
		rr = fakeRR{Subname: sub, Rectype: "CAA", Content: flags + " " + tag + " " + caaQuote(value), Prio: intp(0)}
	default:
		return bad("NO_SUCH_COMMAND", "unsupported zone function")
	}
	if rr.Subname == "" || rr.Content == "" {
		return bad("PARAMETER_MISSING", "subdomain and value are required")
	}
	for _, cur := range z.RRs {
		if cur.Subname != rr.Subname {
			continue
		}
		if (rr.Rectype == "CNAME") != (cur.Rectype == "CNAME") || (rr.Rectype == "CNAME" && !cur.same(rr)) {
			return fakeDomainError(dname, z.ServiceID, "CONFLICT_CNAME", "Can not set CNAME record together with other record for one subdomain", map[string]any{
				"conflicting_records": []map[string]string{{"data": cur.Content, "rectype": cur.Rectype, "subdomain": cur.Subname}},
				"record_to_add":       map[string]string{"data": rr.Content, "rectype": rr.Rectype, "subdomain": rr.Subname},
			})
		}
	}
	for _, cur := range z.RRs {
		if cur.same(rr) {
			return ok
		}
	}
	z.RRs = append(z.RRs, rr)
	return ok
}

func newTestDeps(t *testing.T, api *fakeAPI) *Deps {
	t.Helper()
	env, _, _ := testEnv(map[string]string{"REGRU_USERNAME": "user", "REGRU_PASSWORD": "pw"})
	base := t.TempDir()
	env.Home = base
	clk := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	baseURL := "http://127.0.0.1:1"
	if api != nil {
		clk = api.clock
		baseURL = api.URL
	}
	env.Now = clk.Now
	d := NewDeps(env)
	d.Paths = config.Paths{
		Config: filepath.Join(base, "config", "regru"),
		Cache:  filepath.Join(base, "cache", "regru"),
		State:  filepath.Join(base, "state", "regru"),
	}
	d.Limiter = regruapi.NewLimiter(3*time.Second, clk)
	d.NewClient = func(a config.Account, lim *regruapi.Limiter, rt time.Duration) *regruapi.Client {
		return regruapi.New(regruapi.Credentials{Username: a.Username, Password: a.Password}, lim,
			regruapi.WithBaseURL(baseURL), regruapi.WithClock(clk), regruapi.WithRequestTimeout(rt))
	}
	return d
}

func withEnv(d *Deps, kv ...string) {
	extra := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		extra[kv[i]] = kv[i+1]
	}
	prev := d.Env.LookupEnv
	d.Env.LookupEnv = func(k string) (string, bool) {
		if v, ok := extra[k]; ok {
			return v, true
		}
		return prev(k)
	}
}

func writeConfig(t *testing.T, d *Deps, body string) string {
	t.Helper()
	if err := os.MkdirAll(d.Paths.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d.Paths.Config, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func stdoutOf(d *Deps) *bytes.Buffer { return d.Env.Stdout.(*bytes.Buffer) }

func stderrOf(d *Deps) *bytes.Buffer { return d.Env.Stderr.(*bytes.Buffer) }
