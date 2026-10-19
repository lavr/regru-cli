package cli

import (
	"fmt"
	"io"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var Version, Commit, Date = "dev", "none", "unknown"

func buildVersion(info *debug.BuildInfo, ok bool) (string, string, string) {
	version, commit, date := Version, Commit, Date
	if version != "dev" || !ok || info == nil {
		return version, commit, date
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.time":
			date = s.Value
		}
	}
	return version, commit, date
}

func newVersionCmd(st *state) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			version, commit, date := buildVersion(debug.ReadBuildInfo())
			data := map[string]string{"version": version, "commit": commit, "date": date}
			st.exit = st.output.Done(StatusSuccess, data, nil, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "regru %s (commit %s, built %s)\n", version, commit, date)
				return err
			})
			return nil
		},
	}
}
