package config

import (
	"errors"
	"io/fs"
	"testing"
)

func fakeFS(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
}

func lk(m map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

const baseCfg = `version: 1
default_account: alpha
accounts:
  alpha:
    username: alice
    password: env:REGRU_ALPHA_PASSWORD
    env_file: .env
`

func load(t *testing.T, body string) *Config {
	t.Helper()
	c, err := Load("/cfg/config.yaml", true, fakeFS(map[string]string{"/cfg/config.yaml": body}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPasswordLiteralRejected(t *testing.T) {
	_, err := Load("/cfg/config.yaml", true, fakeFS(map[string]string{"/cfg/config.yaml": "version: 1\naccounts:\n  alpha:\n    username: a\n    password: hunter2\n"}))
	if code(err) != "password_literal" {
		t.Fatalf("got %v", err)
	}
	if contains(err.Error(), "hunter2") {
		t.Fatal("secret leaked")
	}
	_, err = Load("/cfg/config.yaml", true, fakeFS(map[string]string{"/cfg/config.yaml": "version: 1\naccounts:\n  alpha:\n    username: a\n    password: vault:X\n"}))
	if code(err) != "unknown_ref" {
		t.Fatalf("got %v", err)
	}
	_, err = Load("/cfg/config.yaml", true, fakeFS(map[string]string{"/cfg/config.yaml": "version: 1\naccounts:\n  alpha:\n    username: a\n"}))
	if code(err) != "password_literal" {
		t.Fatalf("got %v", err)
	}
}

func TestUsernameEnvRef(t *testing.T) {
	c := load(t, "version: 1\naccounts:\n  alpha:\n    username: env:U\n    password: env:P\n    env_file: .env\n")
	a, err := c.Resolve("alpha", lk(map[string]string{"U": "proc-user", "P": "p"}), fakeFS(map[string]string{"/cfg/.env": "U=file-user\n"}))
	if err != nil || a.Username != "proc-user" {
		t.Fatalf("%v %+v", err, a)
	}
	a, err = c.Resolve("alpha", lk(map[string]string{"P": "p"}), fakeFS(map[string]string{"/cfg/.env": "U=file-user\n"}))
	if err != nil || a.Username != "file-user" {
		t.Fatalf("%v %+v", err, a)
	}
	u, err := c.ResolveUsername("alpha", lk(nil), fakeFS(map[string]string{"/cfg/.env": "U=file-user\n"}))
	if err != nil || u != "file-user" {
		t.Fatalf("%v %q", err, u)
	}
}

func TestUsernameEmptyRejected(t *testing.T) {
	cases := []struct {
		body string
		env  map[string]string
	}{
		{"version: 1\naccounts:\n  alpha:\n    password: env:P\n", map[string]string{"P": "p"}},
		{"version: 1\naccounts:\n  alpha:\n    username: \"\"\n    password: env:P\n", map[string]string{"P": "p"}},
		{"version: 1\naccounts:\n  alpha:\n    username: env:U\n    password: env:P\n", map[string]string{"P": "p", "U": ""}},
		{"version: 1\naccounts:\n  alpha:\n    username: env:U\n    password: env:P\n", map[string]string{"P": "p"}},
	}
	for i, tc := range cases {
		c := load(t, tc.body)
		if _, err := c.Resolve("alpha", lk(tc.env), fakeFS(nil)); code(err) != "missing_username" {
			t.Fatalf("case %d: got %v", i, err)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestEnvFileRelativeToConfigDir(t *testing.T) {
	c := load(t, baseCfg)
	a, err := c.Resolve("alpha", lk(nil), fakeFS(map[string]string{"/cfg/.env": "REGRU_ALPHA_PASSWORD=s3\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Password != "s3" || a.Username != "alice" || a.Name != "alpha" {
		t.Fatalf("%+v", a)
	}
}

func TestEnvFileTilde(t *testing.T) {
	c := load(t, "version: 1\naccounts:\n  alpha:\n    username: a\n    password: env:P\n    env_file: ~/x.env\n")
	a, err := c.Resolve("alpha", lk(map[string]string{"HOME": "/home/u"}), fakeFS(map[string]string{"/home/u/x.env": "P=1"}))
	if err != nil || a.Password != "1" {
		t.Fatalf("%v %+v", err, a)
	}
}

func TestProcessEnvWinsOverEnvFile(t *testing.T) {
	c := load(t, baseCfg)
	a, err := c.Resolve("alpha", lk(map[string]string{"REGRU_ALPHA_PASSWORD": "proc"}), fakeFS(map[string]string{"/cfg/.env": "REGRU_ALPHA_PASSWORD=file"}))
	if err != nil || a.Password != "proc" {
		t.Fatalf("%v %+v", err, a)
	}
}

func TestExplicitEmptyProcessEnvIsMissing(t *testing.T) {
	c := load(t, baseCfg)
	_, err := c.Resolve("alpha", lk(map[string]string{"REGRU_ALPHA_PASSWORD": ""}), fakeFS(map[string]string{"/cfg/.env": "REGRU_ALPHA_PASSWORD=file"}))
	if code(err) != "missing_env" {
		t.Fatalf("got %v", err)
	}
}

func TestMissingEnvVar(t *testing.T) {
	c := load(t, baseCfg)
	_, err := c.Resolve("alpha", lk(nil), fakeFS(map[string]string{"/cfg/.env": "OTHER=1"}))
	if code(err) != "missing_env" || !contains(err.Error(), "REGRU_ALPHA_PASSWORD") {
		t.Fatalf("got %v", err)
	}
	_, err = c.Resolve("alpha", lk(nil), fakeFS(nil))
	if code(err) != "env_file_error" {
		t.Fatalf("got %v", err)
	}
	if _, err = c.Resolve("nope", lk(nil), fakeFS(nil)); code(err) != "unknown_account" {
		t.Fatalf("got %v", err)
	}
}

func TestReservedNames(t *testing.T) {
	for _, n := range []string{"env", "auto"} {
		_, err := Load("/c.yaml", true, fakeFS(map[string]string{"/c.yaml": "version: 1\naccounts:\n  " + n + ":\n    username: a\n    password: env:P\n"}))
		if code(err) != "reserved_name" {
			t.Fatalf("%s: %v", n, err)
		}
	}
}

func TestUnknownField(t *testing.T) {
	_, err := Load("/c.yaml", true, fakeFS(map[string]string{"/c.yaml": "version: 1\nbogus: 1\n"}))
	if code(err) != "config_invalid" {
		t.Fatalf("got %v", err)
	}
	_, err = Load("/c.yaml", true, fakeFS(map[string]string{"/c.yaml": "version: 2\n"}))
	if code(err) != "config_invalid" {
		t.Fatalf("got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	c, err := Load("/c.yaml", false, fakeFS(nil))
	if c != nil || err != nil {
		t.Fatalf("%v %v", c, err)
	}
	if _, err = Load("/c.yaml", true, fakeFS(nil)); code(err) != "config_not_found" {
		t.Fatalf("got %v", err)
	}
}

func TestNoConfigEnvMode(t *testing.T) {
	s, err := Select(nil, "", lk(nil))
	if err != nil || s.Name != "env" {
		t.Fatalf("%v %+v", err, s)
	}
	s, err = Select(nil, "env", lk(nil))
	if err != nil || s.Name != "env" || !s.Explicit {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err = Select(nil, "alpha", lk(nil)); code(err) != "config_not_found" {
		t.Fatalf("got %v", err)
	}
	if _, err = Select(nil, "", lk(map[string]string{"REGRU_ACCOUNT": "alpha"})); code(err) != "config_not_found" {
		t.Fatalf("got %v", err)
	}
	a, err := ResolveEnvMode(lk(map[string]string{"REGRU_USERNAME": "u", "REGRU_PASSWORD": "p"}))
	if err != nil || a.Name != "env" || a.Username != "u" || a.Password != "p" {
		t.Fatalf("%v %+v", err, a)
	}
	if _, err = ResolveEnvMode(lk(map[string]string{"REGRU_USERNAME": "u", "REGRU_PASSWORD": ""})); code(err) != "missing_env" {
		t.Fatalf("got %v", err)
	}
	if _, err = ResolveEnvMode(lk(nil)); code(err) != "missing_env" {
		t.Fatalf("got %v", err)
	}
}

func TestConfigPresentIgnoresGenericEnv(t *testing.T) {
	c := load(t, baseCfg)
	env := map[string]string{"REGRU_USERNAME": "u", "REGRU_PASSWORD": "p", "REGRU_ALPHA_PASSWORD": "real"}
	s, err := Select(c, "", lk(env))
	if err != nil || s.Name != "alpha" || s.Explicit {
		t.Fatalf("%v %+v", err, s)
	}
	a, err := c.Resolve(s.Name, lk(env), fakeFS(nil))
	if err != nil || a.Password != "real" || a.Username != "alice" {
		t.Fatalf("%v %+v", err, a)
	}
	s, _ = Select(c, "", lk(map[string]string{"REGRU_ACCOUNT": "beta"}))
	if s.Name != "beta" || !s.Explicit {
		t.Fatalf("%+v", s)
	}
	s, _ = Select(c, "gamma", lk(map[string]string{"REGRU_ACCOUNT": "beta"}))
	if s.Name != "gamma" || !s.Explicit {
		t.Fatalf("%+v", s)
	}
	if _, err = Select(c, "env", lk(nil)); code(err) != "reserved_name" {
		t.Fatalf("got %v", err)
	}
	if s, err = Select(c, "auto", lk(nil)); err != nil || s.Name != "auto" {
		t.Fatalf("%v %+v", err, s)
	}
}

func TestXDGRelativeIgnored(t *testing.T) {
	p := DefaultPaths(lk(map[string]string{"XDG_CONFIG_HOME": "rel", "XDG_CACHE_HOME": "", "XDG_STATE_HOME": "/s"}), "/home/u")
	if p.Config != "/home/u/.config/regru" || p.Cache != "/home/u/.cache/regru" || p.State != "/s/regru" {
		t.Fatalf("%+v", p)
	}
}

func TestMaxDeletesZeroIsExplicit(t *testing.T) {
	c := load(t, "version: 1\ndefaults:\n  max_deletes: 0\n  timeout: 5m\n  request_timeout: 30s\n")
	d := c.Defaults
	if d.MaxDeletes == nil || *d.MaxDeletes != 0 || d.Timeout == nil || d.Timeout.Minutes() != 5 || d.RequestTimeout == nil || d.RequestTimeout.Seconds() != 30 {
		t.Fatalf("%+v", d)
	}
	c = load(t, "version: 1\n")
	if c.Defaults.MaxDeletes != nil || c.Defaults.Timeout != nil {
		t.Fatal("expected nil")
	}
}
