package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Error struct{ Code, Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

type Defaults struct {
	MaxDeletes     *int
	Timeout        *time.Duration
	RequestTimeout *time.Duration
}

type AccountSpec struct {
	Name, Username, Password, EnvFile string
}

type Account struct {
	Name, Username, Password string
}

type Config struct {
	Version        int
	DefaultAccount string
	Accounts       map[string]AccountSpec
	Defaults       Defaults
	Dir            string
}

type rawAccount struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	EnvFile  string `yaml:"env_file"`
}

type rawDefaults struct {
	MaxDeletes     *int    `yaml:"max_deletes"`
	Timeout        *string `yaml:"timeout"`
	RequestTimeout *string `yaml:"request_timeout"`
}

type rawConfig struct {
	Version        int                   `yaml:"version"`
	DefaultAccount string                `yaml:"default_account"`
	Accounts       map[string]rawAccount `yaml:"accounts"`
	Defaults       rawDefaults           `yaml:"defaults"`
}

func parseDuration(field string, s *string) (*time.Duration, error) {
	if s == nil {
		return nil, nil
	}
	d, err := time.ParseDuration(*s)
	if err != nil || d <= 0 {
		return nil, errf("config_invalid", "defaults.%s: invalid duration", field)
	}
	return &d, nil
}

func Load(path string, explicit bool, readFile func(string) ([]byte, error)) (*Config, error) {
	b, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if explicit {
				return nil, errf("config_not_found", "config file not found: %s", path)
			}
			return nil, nil
		}
		return nil, errf("config_invalid", "cannot read config %s", path)
	}
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, errf("config_invalid", "invalid config %s: %v", path, err)
	}
	if raw.Version != 1 {
		return nil, errf("config_invalid", "unsupported config version %d", raw.Version)
	}
	c := &Config{
		Version:        raw.Version,
		DefaultAccount: raw.DefaultAccount,
		Accounts:       map[string]AccountSpec{},
		Dir:            filepath.Dir(path),
	}
	for name, a := range raw.Accounts {
		if name == "env" || name == "auto" {
			return nil, errf("reserved_name", "account name %q is reserved", name)
		}
		if err := checkPasswordRef(name, a.Password); err != nil {
			return nil, err
		}
		c.Accounts[name] = AccountSpec{Name: name, Username: a.Username, Password: a.Password, EnvFile: a.EnvFile}
	}
	c.Defaults.MaxDeletes = raw.Defaults.MaxDeletes
	if c.Defaults.Timeout, err = parseDuration("timeout", raw.Defaults.Timeout); err != nil {
		return nil, err
	}
	if c.Defaults.RequestTimeout, err = parseDuration("request_timeout", raw.Defaults.RequestTimeout); err != nil {
		return nil, err
	}
	return c, nil
}

func checkPasswordRef(name, ref string) error {
	varName, isEnv := strings.CutPrefix(ref, "env:")
	if !isEnv {
		if ref != "" && strings.Contains(ref, ":") && !strings.ContainsAny(ref, " \t") && isRefPrefix(ref) {
			return errf("unknown_ref", "account %q: unsupported password reference scheme", name)
		}
		return errf("password_literal", "account %q: password must be an env:VAR reference, not a literal", name)
	}
	if varName == "" {
		return errf("unknown_ref", "account %q: empty variable name in password reference", name)
	}
	return nil
}

func (c *Config) lookupVar(name string, spec AccountSpec, varName string, lookup Lookup, readFile func(string) ([]byte, error)) (string, bool, error) {
	if v, set := lookup(varName); set {
		return v, true, nil
	}
	if spec.EnvFile == "" {
		return "", false, nil
	}
	home, _ := lookup("HOME")
	p := expandHome(spec.EnvFile, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.Dir, p)
	}
	b, err := readFile(p)
	if err != nil {
		return "", false, errf("env_file_error", "account %q: cannot read env_file %s", name, p)
	}
	m, err := ParseDotenv(b)
	if err != nil {
		return "", false, errf("env_file_error", "account %q: env_file %s: %v", name, p, err)
	}
	v, ok := m[varName]
	return v, ok, nil
}

func (c *Config) ResolveUsername(name string, lookup Lookup, readFile func(string) ([]byte, error)) (string, error) {
	spec, ok := c.Accounts[name]
	if !ok {
		return "", errf("unknown_account", "account %q not found in config", name)
	}
	varName, isEnv := strings.CutPrefix(spec.Username, "env:")
	if !isEnv {
		if spec.Username == "" {
			return "", errf("missing_username", "account %q: username is empty", name)
		}
		return spec.Username, nil
	}
	if varName == "" {
		return "", errf("unknown_ref", "account %q: empty variable name in username reference", name)
	}
	v, _, err := c.lookupVar(name, spec, varName, lookup, readFile)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", errf("missing_username", "account %q: username variable %s is not set or empty", name, varName)
	}
	return v, nil
}

func (c *Config) Resolve(name string, lookup Lookup, readFile func(string) ([]byte, error)) (Account, error) {
	spec, ok := c.Accounts[name]
	if !ok {
		return Account{}, errf("unknown_account", "account %q not found in config", name)
	}
	if err := checkPasswordRef(name, spec.Password); err != nil {
		return Account{}, err
	}
	username, err := c.ResolveUsername(name, lookup, readFile)
	if err != nil {
		return Account{}, err
	}
	varName := strings.TrimPrefix(spec.Password, "env:")
	v, set, err := c.lookupVar(name, spec, varName, lookup, readFile)
	if err != nil {
		return Account{}, err
	}
	if v == "" {
		if set {
			return Account{}, errf("missing_env", "account %q: variable %s is set but empty", name, varName)
		}
		return Account{}, errf("missing_env", "account %q: variable %s is not set", name, varName)
	}
	return Account{Name: name, Username: username, Password: v}, nil
}

func isRefPrefix(s string) bool {
	prefix, _, _ := strings.Cut(s, ":")
	if prefix == "" {
		return false
	}
	for _, r := range prefix {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func ResolveEnvMode(lookup Lookup) (Account, error) {
	u, _ := lookup("REGRU_USERNAME")
	if u == "" {
		return Account{}, errf("missing_env", "REGRU_USERNAME is not set or empty")
	}
	p, _ := lookup("REGRU_PASSWORD")
	if p == "" {
		return Account{}, errf("missing_env", "REGRU_PASSWORD is not set or empty")
	}
	return Account{Name: "env", Username: u, Password: p}, nil
}

type Selection struct {
	Name     string
	Explicit bool
}

func Select(c *Config, flag string, lookup Lookup) (Selection, error) {
	var sel Selection
	if flag != "" {
		sel = Selection{Name: flag, Explicit: true}
	} else if v, _ := lookup("REGRU_ACCOUNT"); v != "" {
		sel = Selection{Name: v, Explicit: true}
	}
	if c == nil {
		if sel.Name == "" {
			return Selection{Name: "env"}, nil
		}
		if sel.Name == "env" {
			return sel, nil
		}
		return Selection{}, errf("config_not_found", "account %q requested but no config file found", sel.Name)
	}
	if sel.Name == "" {
		sel = Selection{Name: c.DefaultAccount}
	}
	if sel.Name == "env" {
		return Selection{}, errf("reserved_name", "account name %q is reserved and unavailable when a config file exists", "env")
	}
	if sel.Name == "" {
		return Selection{}, errf("account_required", "no account selected: use --account, REGRU_ACCOUNT or default_account")
	}
	return sel, nil
}
