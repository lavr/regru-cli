package cli

import "testing"

func t11TwoDomains(api *fakeAPI) {
	api.addUser("user", "pw", 1000, "example.test", "example.net", "gone.example.test")
	api.addZone("example.net", "1000002")
	api.setNSS("example.net", "ns1.example.net", "ns2.example.net")
}

func TestDomainListZoneManaged(t *testing.T) {
	api := newFakeAPI(t)
	t11TwoDomains(api)
	d := newTestDeps(t, api)
	if code := t11Run(d, "domain", "list", "--output", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderrOf(d).String())
	}
	got := map[string]map[string]any{}
	for _, x := range t11Data(t, d)["domains"].([]any) {
		m := x.(map[string]any)
		got[m["domain"].(string)] = m
	}
	if got["example.test"]["zone_managed"] != true || got["example.test"]["service_id"] != "1000001" {
		t.Fatalf("%v", got["example.test"])
	}
	if got["example.net"]["zone_managed"] != false || len(got["example.net"]["ns"].([]any)) != 2 {
		t.Fatalf("%v", got["example.net"])
	}
	g := got["gone.example.test"]
	if g["ns"] != nil || g["zone_managed"] != nil {
		t.Fatalf("%v", g)
	}
}

func TestDomainListNoNS(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	if code := t11Run(d, "domain", "list", "--no-ns", "--output", "json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := api.Requests.Load(); n != 1 {
		t.Fatalf("%d requests", n)
	}
	x := t11Data(t, d)["domains"].([]any)[0].(map[string]any)
	if x["ns"] != nil || x["zone_managed"] != nil || x["domain"] != "example.test" {
		t.Fatalf("%v", x)
	}
}

func TestDomainListAutoNotAllowed(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, t11Config)
	withEnv(d, "A_PW", "pw")
	if code := t11Run(d, "domain", "list", "--account", "auto", "--output", "json"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].Code != "auto_not_allowed" {
		t.Fatalf("%+v", e)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestDomainGetNormalizes(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	if code := t11Run(d, "domain", "get", "Example.TEST.", "--output", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderrOf(d).String())
	}
	m := t11Data(t, d)
	if m["domain"] != "example.test" || m["zone_managed"] != true || m["service_id"] != "1000001" {
		t.Fatalf("%v", m)
	}
}

func TestDomainGetUnknownDomain(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	if code := t11Run(d, "domain", "get", "other.example.test", "--output", "json"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].Code != "domain_error" {
		t.Fatalf("%+v", e)
	}
}
