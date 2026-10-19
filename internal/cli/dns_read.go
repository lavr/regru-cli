package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lavr/regru-cli/internal/plan"
	"github.com/lavr/regru-cli/internal/regruzone"
	"github.com/lavr/regru-cli/internal/zone"
)

type dnsRecordJSON struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type dnsRawJSON struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Prio    *int   `json:"prio"`
	Count   int    `json:"count"`
}

type dnsSOAJSON struct {
	TTL        int `json:"ttl"`
	MinimumTTL int `json:"minimum_ttl"`
}

func dnsReadSnapshot(cmd *cobra.Command, d *Deps, domainArg string) (zone.Snapshot, string, string, error) {
	s, err := d.LoadSettings(cmd)
	if err != nil {
		return zone.Snapshot{}, "", "", err
	}
	name, err := zone.NormalizeZone(domainArg)
	if err != nil {
		return zone.Snapshot{}, "", "", err
	}
	ctx, cancel := d.Context(s)
	defer cancel()
	sess, err := d.Open(ctx, s, Target{Domain: name, Mutating: false})
	if err != nil {
		return zone.Snapshot{}, "", "", err
	}
	snap, sid, err := dnsFetch(ctx, sess, name)
	if err != nil {
		return zone.Snapshot{}, "", "", err
	}
	return snap, sid, sess.Account.Name, nil
}

func dnsFetch(ctx context.Context, sess *Session, name string) (zone.Snapshot, string, error) {
	return regruzone.NewBackend(sess.Client, name).Snapshot(ctx)
}

func addDNSReadCommands(parent *cobra.Command, d *Deps) {
	get := &cobra.Command{
		Use:   "get <domain>",
		Short: "Show records of a DNS zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			nameFlag, _ := cmd.Flags().GetString("name")
			typeFlag, _ := cmd.Flags().GetString("type")
			var wantType zone.Type
			if typeFlag != "" {
				wantType = zone.Type(strings.ToUpper(typeFlag))
				if _, ok := cliRules().Caps[wantType]; !ok {
					return usageError(fmt.Sprintf("unsupported record type %q", typeFlag))
				}
			}
			zoneName, err := zone.NormalizeZone(args[0])
			if err != nil {
				return err
			}
			wantName := ""
			if nameFlag != "" {
				if wantName, err = zone.NormalizeName(zoneName, nameFlag); err != nil {
					return err
				}
			}
			snap, sid, _, err := dnsReadSnapshot(cmd, d, args[0])
			if err != nil {
				return err
			}
			recs := []dnsRecordJSON{}
			for _, r := range snap.Sorted().Records {
				if wantName != "" && r.Name != wantName {
					continue
				}
				if wantType != "" && r.Type != wantType {
					continue
				}
				recs = append(recs, dnsRecordJSON{Name: r.Name, Type: string(r.Type), Value: r.Value})
			}
			raw := []dnsRawJSON{}
			for _, r := range snap.Raw {
				raw = append(raw, dnsRawJSON{Name: r.Name, Type: r.Type, Content: r.Content, Prio: r.Prio, Count: r.Count})
			}
			var soa *dnsSOAJSON
			if snap.SOA != nil {
				soa = &dnsSOAJSON{TTL: snap.SOA.TTL, MinimumTTL: snap.SOA.MinimumTTL}
			}
			data := map[string]any{"zone": snap.Zone, "service_id": sid, "records": recs, "raw": raw, "soa": soa}
			st.exit = st.output.Done(StatusSuccess, data, nil, func(w io.Writer) error {
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				out := &textWriter{w: tw}
				out.printf("NAME\tTYPE\tVALUE\n")
				for _, r := range recs {
					out.printf("%s\t%s\t%s\n", r.Name, r.Type, r.Value)
				}
				if out.err != nil {
					return out.err
				}
				return tw.Flush()
			})
			return nil
		},
	}
	get.Flags().String("name", "", "show only records with this name")
	get.Flags().String("type", "", "show only records of this type")

	export := &cobra.Command{
		Use:   "export <domain>",
		Short: "Export a DNS zone as a manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := cmdState(cmd)
			file, _ := cmd.Flags().GetString("file")
			scopeFlag, _ := cmd.Flags().GetString("scope")
			scope := plan.Scope(scopeFlag)
			if scope != plan.ScopeListed && scope != plan.ScopeManaged {
				return usageError(fmt.Sprintf("invalid --scope %q: expected %s or %s", scopeFlag, plan.ScopeListed, plan.ScopeManaged))
			}
			snap, _, account, err := dnsReadSnapshot(cmd, d, args[0])
			if err != nil {
				return err
			}
			b, err := plan.ExportManifest(account, snap, scope, cliRules())
			if err != nil {
				return err
			}
			if file == "" {
				st.exit = st.output.Done(StatusSuccess, map[string]any{"manifest": string(b)}, nil, func(w io.Writer) error {
					_, err := w.Write(b)
					return err
				})
				return nil
			}
			if err := os.WriteFile(file, b, 0o644); err != nil {
				return err
			}
			keys := map[zone.Key]bool{}
			for _, r := range snap.Records {
				keys[r.Key()] = true
			}
			st.exit = st.output.Done(StatusSuccess, map[string]any{"file": file, "rrsets": len(keys)}, nil, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "wrote %d rrsets to %s\n", len(keys), file)
				return err
			})
			return nil
		},
	}
	export.Flags().String("file", "", "write the manifest to this file")
	export.Flags().String("scope", string(plan.ScopeListed), "manifest scope: listed-rrsets or managed-zone")

	parent.AddCommand(get, export)
}
