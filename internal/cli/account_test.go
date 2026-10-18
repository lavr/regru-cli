package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func t11Run(d *Deps, args ...string) int {
	return Execute(args, d.Env, d)
}

func t11Data(t *testing.T, d *Deps) map[string]any {
	t.Helper()
	e := decodeOne(t, stdoutOf(d))
	b, _ := json.Marshal(e.Data)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("data: %v", err)
	}
	return m
}

const t11Config = `version: 1
default_account: a
accounts:
  a:
    username: user
    password: env:A_PW
  b:
    username: userb
    password: env:B_PW
    env_file: secrets.env
`

func TestAccountListNoSecrets(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, t11Config)
	withEnv(d, "A_PW", "supersecret-value")
	if code := t11Run(d, "account", "list", "--output", "json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(stdoutOf(d).String(), "supersecret-value") {
		t.Fatal("secret leaked")
	}
	m := t11Data(t, d)
	accs := m["accounts"].([]any)
	if len(accs) != 2 {
		t.Fatalf("%v", accs)
	}
	a := accs[0].(map[string]any)
	if a["name"] != "a" || a["username_ref"] != "user" || a["password_ref"] != "env:A_PW" || a["env_file"] != "" {
		t.Fatalf("%v", a)
	}
	if accs[1].(map[string]any)["env_file"] != "secrets.env" {
		t.Fatalf("%v", accs[1])
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestAccountCheckPartnerFalse(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	if code := t11Run(d, "account", "check", "--output", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderrOf(d).String())
	}
	m := t11Data(t, d)
	if m["account"] != "env" || m["login"] != "user" || m["user_id"] != "1000" || m["partner"] != false {
		t.Fatalf("%v", m)
	}
}

func TestAccountCheckPartnerTrue(t *testing.T) {
	api := newFakeAPI(t)
	api.setPartner("user", true)
	d := newTestDeps(t, api)
	if code := t11Run(d, "account", "check", "--output", "json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if m := t11Data(t, d); m["partner"] != true {
		t.Fatalf("%v", m)
	}
}

func TestMissingEnvNoRequests(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, "version: 1\ndefault_account: a\naccounts:\n  a:\n    username: user\n    password: env:NOPE\n")
	for _, args := range [][]string{{"account", "check"}, {"domain", "list"}, {"domain", "get", "example.test"}} {
		if code := t11Run(d, append(args, "--output", "json")...); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
	if !strings.Contains(stdoutOf(d).String(), `"missing_env"`) {
		t.Fatalf("%s", stdoutOf(d).String())
	}
}

func TestEnvFileUnreadableNoRequests(t *testing.T) {
	api := newFakeAPI(t)
	d := newTestDeps(t, api)
	writeConfig(t, d, "version: 1\ndefault_account: a\naccounts:\n  a:\n    username: user\n    password: env:NOPE\n    env_file: /nonexistent/example.env\n")
	if code := t11Run(d, "account", "check", "--output", "json"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	e := decodeOne(t, stdoutOf(d))
	if len(e.Errors) != 1 || e.Errors[0].Code != "env_file_error" {
		t.Fatalf("%+v", e)
	}
	if n := api.Requests.Load(); n != 0 {
		t.Fatalf("%d requests", n)
	}
}
