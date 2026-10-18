package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

type domainInfo struct {
	Domain      string   `json:"domain"`
	ServiceID   string   `json:"service_id"`
	NS          []string `json:"ns"`
	ZoneManaged *bool    `json:"zone_managed"`
}

func isRegruManaged(nss []string) bool {
	have := map[string]bool{}
	for _, n := range nss {
		have[strings.TrimSuffix(strings.ToLower(n), ".")] = true
	}
	return len(have) == 2 && have["ns1.reg.ru"] && have["ns2.reg.ru"]
}

func nsInfo(n regruapi.DomainNSS) domainInfo {
	info := domainInfo{Domain: n.Domain, ServiceID: n.ServiceID}
	if n.Err != nil {
		return info
	}
	info.NS = append([]string{}, n.NSS...)
	m := isRegruManaged(n.NSS)
	info.ZoneManaged = &m
	return info
}

func writeDomainText(w io.Writer, ds []domainInfo) {
	for _, x := range ds {
		ns, zm := "-", "-"
		if x.NS != nil {
			ns = strings.Join(x.NS, ",")
		}
		if x.ZoneManaged != nil {
			zm = fmt.Sprint(*x.ZoneManaged)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", x.Domain, x.ServiceID, ns, zm)
	}
}

func addDomainCommands(parent *cobra.Command, d *Deps) {
	list := &cobra.Command{
		Use:   "list",
		Short: "List domains of the account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			noNS, _ := cmd.Flags().GetBool("no-ns")
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
			services, err := sess.Client.ListDomains(ctx)
			if err != nil {
				return err
			}
			infos := make([]domainInfo, 0, len(services))
			for _, sv := range services {
				infos = append(infos, domainInfo{Domain: sv.Domain, ServiceID: sv.ServiceID})
			}
			if !noNS && len(services) > 0 {
				names := make([]string, 0, len(services))
				for _, sv := range services {
					names = append(names, sv.Domain)
				}
				nss, err := sess.Client.GetNSS(ctx, names)
				if err != nil {
					return err
				}
				byName := map[string]regruapi.DomainNSS{}
				for _, n := range nss {
					byName[n.Domain] = n
				}
				for i := range infos {
					if n, ok := byName[infos[i].Domain]; ok {
						got := nsInfo(n)
						infos[i].NS, infos[i].ZoneManaged = got.NS, got.ZoneManaged
					}
				}
			}
			st.exit = st.output.Done(StatusSuccess, map[string]any{"domains": infos}, nil, func(w io.Writer) {
				writeDomainText(w, infos)
			})
			return nil
		},
	}
	list.Flags().Bool("no-ns", false, "skip nameserver lookup")
	parent.AddCommand(list)
	parent.AddCommand(&cobra.Command{
		Use:   "get <domain>",
		Short: "Show nameservers and service id of a domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			domain, err := zone.NormalizeZone(args[0])
			if err != nil {
				return err
			}
			s, err := d.LoadSettings(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := d.Context(s)
			defer cancel()
			sess, err := d.Open(ctx, s, Target{Domain: domain})
			if err != nil {
				return err
			}
			nss, err := sess.Client.GetNSS(ctx, []string{domain})
			if err != nil {
				return err
			}
			if len(nss) != 1 {
				return &regruapi.IncompleteReadError{Method: "domain/get_nss", Reason: "answer lacks the requested domain"}
			}
			if nss[0].Err != nil {
				return nss[0].Err
			}
			info := nsInfo(nss[0])
			st.exit = st.output.Done(StatusSuccess, info, nil, func(w io.Writer) {
				writeDomainText(w, []domainInfo{info})
			})
			return nil
		},
	})
}
