package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/lavr/regru-cli/internal/apply"
	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruzone"
	"github.com/lavr/regru-cli/internal/zone"
)

const keepJournals = 50

type FlowOpts struct {
	Yes, DryRun, FromStdin bool

	Deps *Deps
	Out  *Output
}

type prepareFunc func(cur zone.Snapshot, serviceID string, acc plan.Account) (plan.Prepared, error)

type mutAccountView struct {
	Alias  string `json:"alias"`
	Login  string `json:"login"`
	UserID string `json:"user_id"`
}

type mutPolicyView struct {
	MaxDeletes     int  `json:"max_deletes"`
	AllowEmptyZone bool `json:"allow_empty_zone"`
}

type mutOpView struct {
	Seq       int    `json:"seq"`
	Op        string `json:"op"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	DependsOn []int  `json:"depends_on"`
}

type mutPlanView struct {
	PlanID    string         `json:"plan_id"`
	Account   mutAccountView `json:"account"`
	Zone      string         `json:"zone"`
	ServiceID string         `json:"service_id"`
	Policy    mutPolicyView  `json:"policy"`
	Deletes   int            `json:"deletes"`
	Ops       []mutOpView    `json:"ops"`
	Warnings  []string       `json:"warnings"`
}

type mutOpResultView struct {
	mutOpView
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type mutExecView struct {
	RunID    string            `json:"run_id"`
	Status   string            `json:"status"`
	Ops      []mutOpResultView `json:"ops"`
	Mismatch *apply.Mismatch   `json:"mismatch"`
}

type mutData struct {
	Plan      *mutPlanView `json:"plan,omitempty"`
	Execution *mutExecView `json:"execution,omitempty"`
	SavedTo   string       `json:"saved_to,omitempty"`
}

type mutReport struct {
	status   string
	exit     int
	data     mutData
	errs     []ErrorItem
	warnings []string
}

func newHexID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func mutOp(op plan.Op) mutOpView {
	return mutOpView{
		Seq: op.Seq, Op: string(op.Kind),
		Name: op.Record.Name, Type: string(op.Record.Type), Value: op.Record.Value,
		DependsOn: append([]int{}, op.DependsOn...),
	}
}

func mutPlan(p plan.Prepared) *mutPlanView {
	v := &mutPlanView{
		PlanID:    p.PlanID,
		Account:   mutAccountView{Alias: p.Account.Alias, Login: p.Account.Login, UserID: p.Account.UserID},
		Zone:      p.Zone,
		ServiceID: p.ServiceID,
		Policy:    mutPolicyView{MaxDeletes: p.Policy.MaxDeletes, AllowEmptyZone: p.Policy.AllowEmptyZone},
		Deletes:   p.Plan.Deletes(),
		Ops:       []mutOpView{},
		Warnings:  append([]string{}, p.Plan.Warnings...),
	}
	for _, op := range p.Plan.Ops {
		v.Ops = append(v.Ops, mutOp(op))
	}
	return v
}

func opLabel(o mutOpView) string {
	return fmt.Sprintf("%s %s %s %q", o.Op, o.Name, o.Type, o.Value)
}

func writePlanText(w io.Writer, v *mutPlanView) {
	if v == nil {
		return
	}
	if len(v.Ops) == 0 {
		fmt.Fprintf(w, "No changes for %s.\n", v.Zone)
		return
	}
	fmt.Fprintf(w, "Plan for %s (%d operations, %d deletes):\n", v.Zone, len(v.Ops), v.Deletes)
	for _, o := range v.Ops {
		sign := "+"
		if o.Op == string(plan.OpRemove) {
			sign = "-"
		}
		fmt.Fprintf(w, "  %s %s %s %q\n", sign, o.Name, o.Type, o.Value)
	}
}

func writeExecText(w io.Writer, v *mutExecView) {
	if v == nil {
		return
	}
	for _, o := range v.Ops {
		fmt.Fprintf(w, "  [%s] %s\n", o.Status, opLabel(o.mutOpView))
	}
	fmt.Fprintf(w, "Result: %s\n", v.Status)
}

func emitMut(o *Output, r mutReport) int {
	if o.Format == "json" {
		o.writeJSON(Envelope{Status: r.status, Data: r.data, Errors: r.errs, Warnings: r.warnings})
		return r.exit
	}
	writePlanText(o.Env.Stdout, r.data.Plan)
	writeExecText(o.Env.Stdout, r.data.Execution)
	if r.data.SavedTo != "" {
		fmt.Fprintf(o.Env.Stdout, "Plan saved to %s\n", r.data.SavedTo)
	}
	for _, w := range r.warnings {
		fmt.Fprintf(o.Env.Stderr, "warning: %s\n", w)
	}
	for _, e := range r.errs {
		fmt.Fprintf(o.Env.Stderr, "error: %s\n", e.Message)
	}
	return r.exit
}

func failMut(o *Output, err error, data mutData) int {
	c := ToCoded(err)
	status := StatusFailed
	if c.Exit == ExitIncomplete {
		status = StatusPartial
	}
	return emitMut(o, mutReport{status: status, exit: c.Exit, data: data, errs: []ErrorItem{c.Item}})
}

func isTTY(env Env) bool {
	return env.IsTTY != nil && env.IsTTY()
}

func confirm(env Env, v *mutPlanView) bool {
	writePlanText(env.Stderr, v)
	fmt.Fprintf(env.Stderr, "Apply %d operations to %s? [y/N] ", len(v.Ops), v.Zone)
	if env.Stdin == nil {
		return false
	}
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func preparePlan(ctx context.Context, s *Session, b *regruzone.Backend, prepare prepareFunc) (plan.Prepared, error) {
	login, uid, err := s.Client.Nop(ctx)
	if err != nil {
		return plan.Prepared{}, err
	}
	return prepareFor(ctx, b, plan.Account{Alias: s.Account.Name, Login: login, UserID: uid}, prepare)
}

func prepareFor(ctx context.Context, b *regruzone.Backend, acc plan.Account, prepare prepareFunc) (plan.Prepared, error) {
	cur, sid, err := b.Snapshot(ctx)
	if err != nil {
		return plan.Prepared{}, err
	}
	return prepare(cur, sid, acc)
}

func runFlow(ctx context.Context, s *Session, zoneName string, prepare func(cur zone.Snapshot, serviceID string, acc plan.Account) (plan.Prepared, error), opts FlowOpts) int {
	d, o := opts.Deps, opts.Out
	b := regruzone.NewBackend(s.Client, zoneName)
	login, uid, err := s.Client.Nop(ctx)
	if err != nil {
		return failMut(o, err, mutData{})
	}
	unlock, err := apply.LockZone(d.Paths.State, uid, zoneName)
	if err != nil {
		return failMut(o, err, mutData{})
	}
	defer unlock()
	p, err := prepareFor(ctx, b, plan.Account{Alias: s.Account.Name, Login: login, UserID: uid}, prepare)
	if err != nil {
		return failMut(o, err, mutData{})
	}
	pv := mutPlan(p)
	data := mutData{Plan: pv}
	warnings := append([]string{}, p.Plan.Warnings...)
	if p.Plan.Empty() || opts.DryRun {
		return emitMut(o, mutReport{status: StatusSuccess, exit: ExitOK, data: data, warnings: warnings})
	}
	if !opts.Yes {
		if !isTTY(o.Env) || opts.FromStdin {
			item := ErrorItem{Code: StatusConfirmationRequired, Message: "changes require confirmation: rerun with --yes", Domain: zoneName}
			return emitMut(o, mutReport{status: StatusConfirmationRequired, exit: ExitFailure, data: data, errs: []ErrorItem{item}, warnings: warnings})
		}
		if !confirm(o.Env, pv) {
			item := ErrorItem{Code: "confirmation_declined", Message: "changes were not confirmed", Domain: zoneName}
			return emitMut(o, mutReport{status: StatusFailed, exit: ExitFailure, data: data, errs: []ErrorItem{item}, warnings: warnings})
		}
	}
	final, err := apply.Verify(ctx, b, p, cliRules())
	if err != nil {
		return failMut(o, err, data)
	}
	runID := newHexID()
	j, err := d.OpenJournal(d.Paths.State, uid, zoneName, runID)
	if err != nil {
		item := ErrorItem{Code: "journal_open_failed", Message: err.Error(), Domain: zoneName}
		return emitMut(o, mutReport{status: StatusFailed, exit: ExitFailure, data: data, errs: []ErrorItem{item}, warnings: warnings})
	}
	res := apply.Execute(ctx, b, j, p, final, runID)
	if c, ok := j.(io.Closer); ok {
		c.Close()
	}
	if err := apply.RotateJournals(filepath.Join(d.Paths.State, "journal", uid, zoneName), keepJournals); err != nil {
		warnings = append(warnings, "journal rotation failed: "+err.Error())
	}
	return emitMut(o, execReport(ctx, zoneName, runID, res, data, warnings))
}

func execReport(ctx context.Context, zoneName, runID string, res apply.Result, data mutData, warnings []string) mutReport {
	ex := &mutExecView{RunID: runID, Status: res.Status, Ops: []mutOpResultView{}, Mismatch: res.Mismatch}
	var errs []ErrorItem
	for _, r := range res.Ops {
		v := mutOpResultView{mutOpView: mutOp(r.Op), Status: r.Status, Detail: r.Detail}
		ex.Ops = append(ex.Ops, v)
		switch r.Status {
		case apply.StatusFailed:
			errs = append(errs, ErrorItem{Code: "operation_failed", Message: r.Detail, Domain: zoneName, Operation: opLabel(v.mutOpView)})
		case apply.StatusUnknown:
			errs = append(errs, ErrorItem{Code: "operation_unknown", Message: r.Detail, Domain: zoneName, Operation: opLabel(v.mutOpView)})
		}
	}
	if res.Mismatch != nil {
		errs = append(errs, ErrorItem{Code: "verification_mismatch", Message: "zone after apply differs from the expected state", Domain: zoneName, Details: res.Mismatch})
	}
	status, exit := res.Status, ExitIncomplete
	switch res.Status {
	case apply.RunSuccess:
		exit = ExitOK
	case apply.RunFailed:
		exit = ExitFailure
	}
	if res.JournalErr != nil {
		errs = append(errs, ErrorItem{Code: "journal_write_failed", Message: res.JournalErr.Error(), Domain: zoneName})
		if status == apply.RunSuccess {
			status = StatusPartial
		}
		if exit != ExitFailure {
			exit = ExitIncomplete
		}
	}
	if res.Status != apply.RunSuccess && len(errs) == 0 {
		code, msg := "apply_"+res.Status, "apply finished with status "+res.Status
		if err := ctx.Err(); err != nil {
			c := ToCoded(err)
			code, msg = c.Item.Code, c.Item.Message
		}
		errs = append(errs, ErrorItem{Code: code, Message: msg, Domain: zoneName})
	}
	data.Execution = ex
	return mutReport{status: status, exit: exit, data: data, errs: errs, warnings: warnings}
}
