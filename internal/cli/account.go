package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

type accountInfo struct {
	Name        string `json:"name"`
	UsernameRef string `json:"username_ref"`
	PasswordRef string `json:"password_ref"`
	EnvFile     string `json:"env_file"`
}

type accountCheck struct {
	Account string `json:"account"`
	Login   string `json:"login"`
	UserID  string `json:"user_id"`
	Partner *bool  `json:"partner"`
}

func addAccountCommands(parent *cobra.Command, d *Deps) {
	parent.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured accounts without secrets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			s, err := d.LoadSettings(cmd)
			if err != nil {
				return err
			}
			accounts := []accountInfo{}
			if s.Config == nil {
				accounts = append(accounts, accountInfo{Name: "env", UsernameRef: "env:REGRU_USERNAME", PasswordRef: "env:REGRU_PASSWORD"})
			} else {
				names := make([]string, 0, len(s.Config.Accounts))
				for n := range s.Config.Accounts {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					a := s.Config.Accounts[n]
					accounts = append(accounts, accountInfo{Name: n, UsernameRef: a.Username, PasswordRef: passwordRef(a.Password), EnvFile: a.EnvFile})
				}
			}
			st.exit = st.output.Done(StatusSuccess, map[string]any{"accounts": accounts}, nil, func(w io.Writer) error {
				tw := &textWriter{w: w}
				for _, a := range accounts {
					tw.printf("%s\t%s\t%s\t%s\n", a.Name, a.UsernameRef, a.PasswordRef, a.EnvFile)
				}
				return tw.err
			})
			return nil
		},
	})
	parent.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Check account credentials against reg.ru",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			s, err := d.LoadSettings(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := d.Context(s)
			defer cancel()
			sess, err := d.Open(ctx, s, Target{})
			if err != nil {
				return err
			}
			login, userID, err := sess.Client.Nop(ctx)
			if err != nil {
				return err
			}
			res := accountCheck{Account: sess.Account.Name, Login: login, UserID: userID}
			var warnings []string
			partner, perr := sess.Client.IsPartner(ctx)
			if perr != nil {
				warnings = append(warnings, "partner status unknown: "+perr.Error())
			} else {
				res.Partner = &partner
			}
			st.exit = st.output.Done(StatusSuccess, res, warnings, func(w io.Writer) error {
				p := "unknown"
				if res.Partner != nil {
					p = fmt.Sprint(*res.Partner)
				}
				_, err := fmt.Fprintf(w, "account: %s\nlogin: %s\nuser_id: %s\npartner: %s\n", res.Account, res.Login, res.UserID, p)
				return err
			})
			return nil
		},
	})
}

func passwordRef(s string) string {
	if strings.HasPrefix(s, "env:") {
		return s
	}
	return ""
}
