package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

var Version, Commit, Date = "dev", "none", "unknown"

func newVersionCmd(st *state) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data := map[string]string{"version": Version, "commit": Commit, "date": Date}
			st.exit = st.output.Done(StatusSuccess, data, nil, func(w io.Writer) {
				fmt.Fprintf(w, "regru %s (commit %s, built %s)\n", Version, Commit, Date)
			})
			return nil
		},
	}
}
