package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lavr/regru-cli/internal/apply"
	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/regruzone"
	"github.com/lavr/regru-cli/internal/zone"
)

const allowEmptyHelp = "a plan that would leave the zone without writable records is refused unless --allow-empty-zone is given"

func addDNSWriteCommands(parent *cobra.Command, d *Deps) {
	parent.AddCommand(
		newDNSPlanCmd(d),
		newDNSApplyCmd(d),
		newRecordCmd(d, "add", "add <domain> <name> <type> <value>...", "Add values to an RRset",
			"Adds the given values to the RRset; existing values are a no-op.", 4),
		newRecordCmd(d, "rm", "rm <domain> <name> <type> (<value>... | --all-values)", "Remove values from an RRset",
			"Removes the given values from the RRset; absent values are a no-op.\n"+allowEmptyHelp+".", 3),
		newRecordCmd(d, "set", "set <domain> <name> <type> <value>...", "Replace an RRset",
			"Sets the whole RRset: missing values are added, other values are removed.\n"+allowEmptyHelp+".", 4),
	)
}

func minArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return usageError(fmt.Sprintf("%s requires at least %d arguments, got %d", cmd.Name(), n, len(args)))
		}
		return nil
	}
}

func addConfirmFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("yes", false, "apply without interactive confirmation")
	cmd.Flags().Bool("dry-run", false, "show the plan without applying it")
}

func addPolicyFlags(cmd *cobra.Command) {
	cmd.Flags().Int("max-deletes", defaultMaxDeletes, "maximum number of records the plan may delete")
	cmd.Flags().Bool("allow-empty-zone", false, "allow a plan that leaves the zone without writable records")
}

func boolFlag(cmd *cobra.Command, name string) bool {
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func stringFlag(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

func unreadable(msg string) error {
	return &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: "file_unreadable", Message: msg}}
}

func readInput(env Env, path string) ([]byte, error) {
	if path == "-" {
		if env.Stdin == nil {
			return nil, unreadable("stdin is not available")
		}
		b, err := io.ReadAll(env.Stdin)
		if err != nil {
			return nil, unreadable("read stdin: " + err.Error())
		}
		return b, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, unreadable(err.Error())
	}
	return b, nil
}

func loadManifest(env Env, path string) (plan.Manifest, error) {
	b, err := readInput(env, path)
	if err != nil {
		return plan.Manifest{}, err
	}
	m, err := plan.ParseManifest(b)
	if err != nil {
		return plan.Manifest{}, err
	}
	if err := plan.ValidateManifest(m, cliRules()); err != nil {
		return plan.Manifest{}, err
	}
	return m, nil
}

func manifestPrepare(m plan.Manifest, maxDeletes int, allowEmpty bool) prepareFunc {
	return func(cur zone.Snapshot, sid string, acc plan.Account) (plan.Prepared, error) {
		pol := plan.Policy{MaxDeletes: maxDeletes, AllowEmptyZone: allowEmpty, Rules: cliRules()}
		return plan.Prepare(acc, m.Zone, sid, cur, m.Desired, pol, newHexID)
	}
}

func savePlan(path string, p plan.Prepared) error {
	var buf bytes.Buffer
	if err := plan.EncodeFile(&buf, p); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func newDNSPlanCmd(d *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan --file F",
		Short: "Show the changes a zone manifest would make",
		Long: "Reads the zone, compares it with the manifest and prints the plan without changing anything.\n" +
			allowEmptyHelp + ".\nWith --detailed-exitcode: 0 no changes, 2 changes, 1 any error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			detailed := boolFlag(cmd, "detailed-exitcode")
			fail := func(err error, data mutData) error {
				code := failMut(st.output, err, data)
				if detailed {
					code = ExitFailure
				}
				st.exit = code
				return nil
			}
			file := stringFlag(cmd, "file")
			if file == "" {
				return fail(usageError("--file is required"), mutData{})
			}
			m, err := loadManifest(st.env, file)
			if err != nil {
				return fail(err, mutData{})
			}
			s, err := d.LoadSettings(cmd)
			if err != nil {
				return fail(err, mutData{})
			}
			ctx, cancel := d.Context(s)
			defer cancel()
			sess, err := d.Open(ctx, s, Target{Domain: m.Zone, Pinned: m.Account, Mutating: true})
			if err != nil {
				return fail(err, mutData{})
			}
			b := regruzone.NewBackend(sess.Client, m.Zone)
			p, err := preparePlan(ctx, sess, b, manifestPrepare(m, s.MaxDeletes, boolFlag(cmd, "allow-empty-zone")))
			if err != nil {
				return fail(err, mutData{})
			}
			data := mutData{Plan: mutPlan(p)}
			if save := stringFlag(cmd, "save"); save != "" {
				if err := savePlan(save, p); err != nil {
					return fail(&CodedError{Exit: ExitFailure, Item: ErrorItem{Code: "plan_save_failed", Message: err.Error()}}, data)
				}
				data.SavedTo = save
			}
			exit := ExitOK
			if detailed && !p.Plan.Empty() {
				exit = ExitUsage
			}
			st.exit = emitMut(st.output, mutReport{status: StatusSuccess, exit: exit, data: data, warnings: append([]string{}, p.Plan.Warnings...)})
			return nil
		},
	}
	cmd.Flags().String("file", "", "zone manifest (- for stdin)")
	cmd.Flags().String("save", "", "write the prepared plan to this file")
	cmd.Flags().Bool("detailed-exitcode", false, "exit 0 without changes, 2 with changes, 1 on error")
	addPolicyFlags(cmd)
	return cmd
}

func planPolicyOverride(cmd *cobra.Command, p plan.Prepared) error {
	if cmd.Flags().Changed("allow-empty-zone") {
		return &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: "policy_change_requires_new_plan",
			Message: "--allow-empty-zone is part of the saved plan policy; build a new plan with it"}}
	}
	if !cmd.Flags().Changed("max-deletes") {
		return nil
	}
	n, _ := cmd.Flags().GetInt("max-deletes")
	if n > p.Policy.MaxDeletes {
		return &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: "policy_change_requires_new_plan",
			Message: fmt.Sprintf("--max-deletes %d is looser than the saved plan limit %d; build a new plan", n, p.Policy.MaxDeletes)}}
	}
	if del := p.Plan.Deletes(); del > n {
		return &plan.PolicyError{Code: "max_deletes_exceeded", Detail: fmt.Sprintf("plan deletes %d records, limit is %d", del, n)}
	}
	return nil
}

func newDNSApplyCmd(d *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply (--plan P | --file F)",
		Short: "Apply a saved plan or a zone manifest",
		Long: "Applies a saved plan (--plan) or a zone manifest (--file), exactly one of them.\n" +
			"The zone is re-read and the plan verified right before any change; drift aborts with exit code 4.\n" +
			allowEmptyHelp + "; with --plan the policy cannot be loosened, only --max-deletes can be tightened.\n" +
			"Without a terminal, or with --file -, --yes is required.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			planPath, file := stringFlag(cmd, "plan"), stringFlag(cmd, "file")
			if (planPath == "") == (file == "") {
				return usageError("exactly one of --plan or --file is required")
			}
			s, err := d.LoadSettings(cmd)
			if err != nil {
				return err
			}
			opts := FlowOpts{Yes: boolFlag(cmd, "yes"), DryRun: boolFlag(cmd, "dry-run"), Deps: d, Out: st.output}
			var (
				zoneName string
				tgt      Target
				prepare  prepareFunc
			)
			if planPath != "" {
				opts.FromStdin = planPath == "-"
				b, err := readInput(st.env, planPath)
				if err != nil {
					return err
				}
				p, err := plan.DecodeFile(bytes.NewReader(b))
				if err != nil {
					return err
				}
				if p.Endpoint != regruapi.Endpoint {
					return &apply.StaleError{Code: "endpoint_mismatch", Detail: map[string]string{"plan": p.Endpoint, "expected": regruapi.Endpoint}}
				}
				if err := planPolicyOverride(cmd, p); err != nil {
					return err
				}
				zoneName = p.Zone
				tgt = Target{Domain: p.Zone, Pinned: p.Account.Alias, Mutating: true}
				prepare = func(cur zone.Snapshot, sid string, acc plan.Account) (plan.Prepared, error) {
					if acc.UserID != p.Account.UserID {
						return plan.Prepared{}, &apply.StaleError{Code: "identity_mismatch", Detail: map[string]string{"plan": p.Account.UserID, "actual": acc.UserID}}
					}
					return p, nil
				}
			} else {
				opts.FromStdin = file == "-"
				m, err := loadManifest(st.env, file)
				if err != nil {
					return err
				}
				zoneName = m.Zone
				tgt = Target{Domain: m.Zone, Pinned: m.Account, Mutating: true}
				prepare = manifestPrepare(m, s.MaxDeletes, boolFlag(cmd, "allow-empty-zone"))
			}
			ctx, cancel := d.Context(s)
			defer cancel()
			sess, err := d.Open(ctx, s, tgt)
			if err != nil {
				return err
			}
			st.exit = runFlow(ctx, sess, zoneName, prepare, opts)
			return nil
		},
	}
	cmd.Flags().String("plan", "", "saved plan file (- for stdin)")
	cmd.Flags().String("file", "", "zone manifest (- for stdin)")
	addConfirmFlags(cmd)
	addPolicyFlags(cmd)
	return cmd
}

func parseRecordArgs(args []string) (string, zone.Key, []string, error) {
	zoneName, err := zone.NormalizeZone(args[0])
	if err != nil {
		return "", zone.Key{}, nil, err
	}
	name, err := zone.NormalizeName(zoneName, args[1])
	if err != nil {
		return "", zone.Key{}, nil, err
	}
	t := zone.Type(strings.ToUpper(args[2]))
	if !cliRules().Caps[t].Write {
		return "", zone.Key{}, nil, &CodedError{Exit: ExitUsage, Item: ErrorItem{Code: "unsupported_type",
			Message: fmt.Sprintf("record type %q cannot be changed", args[2])}}
	}
	var values []string
	seen := map[string]bool{}
	for _, raw := range args[3:] {
		v, err := zone.ParseValue(t, raw)
		if err != nil {
			return "", zone.Key{}, nil, err
		}
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	return zoneName, zone.Key{Name: name, Type: t}, values, nil
}

func newRecordCmd(d *Deps, verb, use, short, long string, nargs int) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long + "\nValues starting with a dash go after --. Without a terminal --yes is required.",
		Args:  minArgs(nargs),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			zoneName, key, values, err := parseRecordArgs(args)
			if err != nil {
				return err
			}
			all := verb == "rm" && boolFlag(cmd, "all-values")
			if verb == "rm" && all == (len(values) > 0) {
				return usageError("rm requires either values or --all-values")
			}
			var desired func(cur zone.Snapshot) plan.Desired
			switch verb {
			case "add":
				desired = func(cur zone.Snapshot) plan.Desired { return plan.DesiredAdd(cur, key, values) }
			case "rm":
				desired = func(cur zone.Snapshot) plan.Desired { return plan.DesiredRemove(cur, key, values, all) }
			default:
				want, err := plan.DesiredSet(key, values)
				if err != nil {
					return err
				}
				desired = func(zone.Snapshot) plan.Desired { return want }
			}
			s, err := d.LoadSettings(cmd)
			if err != nil {
				return err
			}
			allowEmpty := boolFlag(cmd, "allow-empty-zone")
			ctx, cancel := d.Context(s)
			defer cancel()
			sess, err := d.Open(ctx, s, Target{Domain: zoneName, Mutating: true})
			if err != nil {
				return err
			}
			prepare := func(cur zone.Snapshot, sid string, acc plan.Account) (plan.Prepared, error) {
				pol := plan.Policy{MaxDeletes: s.MaxDeletes, AllowEmptyZone: allowEmpty, Rules: cliRules()}
				return plan.Prepare(acc, zoneName, sid, cur, desired(cur), pol, newHexID)
			}
			opts := FlowOpts{Yes: boolFlag(cmd, "yes"), DryRun: boolFlag(cmd, "dry-run"), Deps: d, Out: st.output}
			st.exit = runFlow(ctx, sess, zoneName, prepare, opts)
			return nil
		},
	}
	addConfirmFlags(cmd)
	addPolicyFlags(cmd)
	if verb == "rm" {
		cmd.Flags().Bool("all-values", false, "remove every value of the RRset as read right now")
	}
	return cmd
}
