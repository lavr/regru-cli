package cli

import (
	"context"

	"github.com/spf13/cobra"
)

type stateKey struct{}

func withState(ctx context.Context, st *state) context.Context {
	return context.WithValue(ctx, stateKey{}, st)
}

func cmdState(cmd *cobra.Command) *state {
	st, _ := cmd.Context().Value(stateKey{}).(*state)
	return st
}

func addCommands(root *cobra.Command, d *Deps) {
	acc := &cobra.Command{Use: "account", Short: "Inspect configured reg.ru accounts"}
	dom := &cobra.Command{Use: "domain", Short: "List and inspect domains"}
	dns := &cobra.Command{Use: "dns", Short: "Read and change DNS zones"}
	addAccountCommands(acc, d)
	addDomainCommands(dom, d)
	addDNSReadCommands(dns, d)
	addDNSWriteCommands(dns, d)
	root.AddCommand(acc, dom, dns)
}
