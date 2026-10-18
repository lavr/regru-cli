package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/lavr/regru-cli/internal/apply"
	"github.com/lavr/regru-cli/internal/config"
	"github.com/lavr/regru-cli/internal/regruapi"
)

const (
	limiterInterval       = 3 * time.Second
	defaultTimeout        = 5 * time.Minute
	defaultRequestTimeout = 30 * time.Second
	defaultMaxDeletes     = 10
)

type Deps struct {
	Env       Env
	Paths     config.Paths
	Limiter   *regruapi.Limiter
	NewClient func(config.Account, *regruapi.Limiter, time.Duration) *regruapi.Client
	Discover  func(ctx context.Context, d *Deps, domain string, fresh bool) (string, error)

	OpenJournal func(stateDir, userID, zoneName, runID string) (apply.Journal, error)
	BaseContext func() context.Context

	active *Settings
}

func NewDeps(env Env) *Deps {
	lookup := env.LookupEnv
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	return &Deps{
		Env:       env,
		Paths:     config.DefaultPaths(lookup, env.Home),
		Limiter:   regruapi.NewLimiter(limiterInterval, nil),
		NewClient: defaultNewClient,
		Discover:  Discover,

		OpenJournal: openFileJournal,
	}
}

func openFileJournal(stateDir, userID, zoneName, runID string) (apply.Journal, error) {
	j, err := apply.OpenJournal(stateDir, userID, zoneName, runID)
	if err != nil {
		return nil, err
	}
	return j, nil
}

func defaultNewClient(a config.Account, lim *regruapi.Limiter, requestTimeout time.Duration) *regruapi.Client {
	return regruapi.New(regruapi.Credentials{Username: a.Username, Password: a.Password}, lim,
		regruapi.WithRequestTimeout(requestTimeout))
}

type Target struct {
	Domain   string
	Pinned   string
	Mutating bool
}

type Settings struct {
	Config         *config.Config
	Selection      config.Selection
	MaxDeletes     int
	Timeout        time.Duration
	RequestTimeout time.Duration
}

type Session struct {
	Settings *Settings
	Account  config.Account
	Client   *regruapi.Client
}

func (d *Deps) lookup(k string) (string, bool) {
	if d.Env.LookupEnv == nil {
		return "", false
	}
	return d.Env.LookupEnv(k)
}

func (d *Deps) LoadSettings(cmd *cobra.Command) (*Settings, error) {
	flags := cmd.Flags()
	path, _ := flags.GetString("config")
	explicit := path != ""
	if !explicit {
		if v, ok := d.lookup("REGRU_CONFIG"); ok && v != "" {
			path, explicit = v, true
		}
	}
	if !explicit {
		path = filepath.Join(d.Paths.Config, "config.yaml")
	}
	cfg, err := config.Load(path, explicit, os.ReadFile)
	if err != nil {
		return nil, err
	}
	accountFlag, _ := flags.GetString("account")
	sel, err := config.Select(cfg, accountFlag, d.lookup)
	if err != nil {
		return nil, err
	}
	s := &Settings{Config: cfg, Selection: sel, MaxDeletes: defaultMaxDeletes, Timeout: defaultTimeout, RequestTimeout: defaultRequestTimeout}
	if cfg != nil {
		if cfg.Defaults.MaxDeletes != nil {
			s.MaxDeletes = *cfg.Defaults.MaxDeletes
		}
		if cfg.Defaults.Timeout != nil {
			s.Timeout = *cfg.Defaults.Timeout
		}
		if cfg.Defaults.RequestTimeout != nil {
			s.RequestTimeout = *cfg.Defaults.RequestTimeout
		}
	}
	if f := flags.Lookup("max-deletes"); f != nil && f.Changed {
		n, err := flags.GetInt("max-deletes")
		if err != nil || n < 0 {
			return nil, usageError("--max-deletes must be a non-negative integer")
		}
		s.MaxDeletes = n
	}
	if f := flags.Lookup("timeout"); f != nil && f.Changed {
		t, err := flags.GetDuration("timeout")
		if err != nil || t <= 0 {
			return nil, usageError("--timeout must be a positive duration")
		}
		s.Timeout = t
	}
	if f := flags.Lookup("request-timeout"); f != nil && f.Changed {
		t, err := flags.GetDuration("request-timeout")
		if err != nil || t <= 0 {
			return nil, usageError("--request-timeout must be a positive duration")
		}
		s.RequestTimeout = t
	}
	if s.MaxDeletes < 0 {
		return nil, &config.Error{Code: "config_invalid", Msg: "defaults.max_deletes must not be negative"}
	}
	return s, nil
}

func (d *Deps) Context(s *Settings) (context.Context, context.CancelFunc) {
	base := context.Background()
	if d.BaseContext != nil {
		base = d.BaseContext()
	}
	sctx, stop := signal.NotifyContext(base, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(sctx, s.Timeout)
	return ctx, func() {
		cancel()
		stop()
	}
}

func accountErr(code, msg string) *CodedError {
	return &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: code, Message: msg}}
}

func (d *Deps) Open(ctx context.Context, s *Settings, t Target) (*Session, error) {
	sel := s.Selection
	if t.Pinned != "" {
		if sel.Explicit && sel.Name != "auto" && sel.Name != t.Pinned {
			return nil, accountErr("account_conflict", fmt.Sprintf("account %q was requested, but the plan is bound to account %q", sel.Name, t.Pinned))
		}
		sel = config.Selection{Name: t.Pinned, Explicit: true}
	}
	name := sel.Name
	if name == "auto" {
		if t.Domain == "" {
			return nil, accountErr("auto_not_allowed", "--account auto requires a domain argument")
		}
		if d.Discover == nil {
			return nil, accountErr("auto_not_implemented", "account discovery is not available")
		}
		d.active = s
		found, err := d.Discover(ctx, d, t.Domain, t.Mutating)
		if err != nil {
			return nil, err
		}
		name = found
	}
	acc, err := d.resolve(s, name)
	if err != nil {
		return nil, err
	}
	return &Session{Settings: s, Account: acc, Client: d.NewClient(acc, d.Limiter, s.RequestTimeout)}, nil
}

func (d *Deps) resolve(s *Settings, name string) (config.Account, error) {
	if name == "env" {
		if s.Config != nil {
			return config.Account{}, &config.Error{Code: "reserved_name", Msg: `account name "env" is reserved and unavailable when a config file exists`}
		}
		return config.ResolveEnvMode(d.lookup)
	}
	if s.Config == nil {
		return config.Account{}, &config.Error{Code: "config_not_found", Msg: fmt.Sprintf("account %q requested but no config file found", name)}
	}
	return s.Config.Resolve(name, d.lookup, os.ReadFile)
}
