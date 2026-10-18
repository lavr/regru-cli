package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type state struct {
	env    Env
	deps   *Deps
	output *Output
	exit   int
	ran    bool

	config  string
	account string
	format  string
	timeout time.Duration
	verbose bool
}

func usageError(msg string) *CodedError {
	return &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: "usage", Message: msg}}
}

func validFormat(f string) bool { return f == "text" || f == "json" }

func (st *state) envFormat() string {
	if v, ok := st.env.LookupEnv("REGRU_OUTPUT"); ok {
		return v
	}
	return ""
}

func (st *state) resolveFormat(flagValue string, flagSet bool) (string, error) {
	f := st.envFormat()
	if flagSet {
		f = flagValue
	}
	if f == "" {
		f = "text"
	}
	if !validFormat(f) {
		return "", usageError(fmt.Sprintf("invalid --output %q: expected text or json", f))
	}
	return f, nil
}

func scanOutputFlag(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "--output" && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, "--output="); ok {
			return v, true
		}
	}
	return "", false
}

func NewRoot(env Env, deps *Deps) *cobra.Command {
	if deps == nil {
		deps = NewDeps(env)
	}
	return newRoot(&state{env: env, deps: deps})
}

func newRoot(st *state) *cobra.Command {
	root := &cobra.Command{
		Use:           "regru",
		Short:         "Manage reg.ru DNS zones",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			st.ran = true
			f, err := st.resolveFormat(st.format, cmd.Flags().Changed("output"))
			if err != nil {
				return err
			}
			st.output = &Output{Format: f, Env: st.env}
			return nil
		},
	}
	root.SetOut(st.env.Stderr)
	root.SetErr(st.env.Stderr)
	root.SetIn(st.env.Stdin)
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return usageError(err.Error())
	})
	pf := root.PersistentFlags()
	pf.StringVar(&st.config, "config", "", "path to config file")
	pf.StringVar(&st.account, "account", "", "account name")
	pf.StringVar(&st.format, "output", "", "output format: text or json")
	pf.DurationVar(&st.timeout, "timeout", 0, "overall timeout")
	pf.BoolVarP(&st.verbose, "verbose", "v", false, "verbose diagnostics to stderr")
	root.AddCommand(newVersionCmd(st))
	addCommands(root, st.deps)
	root.SetContext(withState(context.Background(), st))
	return root
}

func Execute(args []string, env Env, deps *Deps) int {
	if deps == nil {
		deps = NewDeps(env)
	}
	st := &state{env: env, deps: deps}
	return run(st, newRoot(st), args)
}

func run(st *state, root *cobra.Command, args []string) int {
	env := st.env
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return st.exit
	}
	var ce *CodedError
	if !errors.As(err, &ce) {
		if st.ran {
			c := ToCoded(err)
			ce = &c
		} else {
			ce = usageError(err.Error())
		}
	}
	flagValue, flagSet := scanOutputFlag(args)
	format, ferr := st.resolveFormat(flagValue, flagSet)
	if ferr != nil {
		format = "text"
		if st.envFormat() == "json" {
			format = "json"
		}
	}
	out := &Output{Format: format, Env: env}
	code := out.Fail(ce, nil)
	if format == "text" && ce.Exit == ExitUsage {
		target, _, ferr := root.Find(args)
		if ferr != nil || target == nil {
			target = root
		}
		fmt.Fprint(env.Stderr, target.UsageString())
	}
	return code
}
