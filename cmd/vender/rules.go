package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/rules"
)

func (a *app) newRulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Inspect the built-in routing rules",
		Long: `Show the routing policy vender applies in proxy.mode: rule.

The policy is built in and fully offline: the rule sets it matches against are
compiled sing-box rule sets embedded in the vender binary, so no download and
no data directory is needed at runtime.

  vender rules list    the ordered policy, the DNS policy and the embedded sets`,
	}
	cmd.AddCommand(a.newRulesListCmd())
	return cmd
}

func (a *app) newRulesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the ordered built-in routing policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "# vender built-in routing policy (proxy.mode: rule)\n")
			fmt.Fprintf(out, "# rule sets embedded from %s @ %s (%s)\n\n",
				rules.RuleSetSourceRepo,
				shortCommit(rules.RuleSetSourceCommit),
				rules.RuleSetSourceDate)

			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "#\tACTION\tMATCH")
			for i, act := range rules.BuiltinActions() {
				fmt.Fprintf(w, "%d\t%s\n", i+1, act.Summary())
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)

			w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "#\tNAME\tMATCHES\tOUTBOUND")
			n := 0
			for _, r := range rules.BuiltinRules() {
				n++
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", n, r.Name, r.Summary(), r.Outbound)
			}
			fmt.Fprintf(w, "\tfinal\t(anything left)\t%s\n", rules.OutProxy)
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)

			dns := rules.BuiltinDNS()
			fmt.Fprintln(out, "DNS")
			w = tabwriter.NewWriter(out, 4, 4, 2, ' ', 0)
			for _, s := range dns.Servers {
				where := "direct"
				if s.Detour != "" {
					where = "via " + s.Detour
				}
				fmt.Fprintf(w, "  server\t%s\t%s %s\t(%s)\n", s.Tag, s.Type, s.Server, where)
			}
			for _, r := range dns.Rules {
				fmt.Fprintf(w, "  rule\t%s\trule_set=[%s]\t-> %s\n", r.Name, joinTags(r.RuleSets), r.Server)
			}
			fmt.Fprintf(w, "  final\t%s\tstrategy %s\n", dns.Final, dns.Strategy)
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)

			fmt.Fprintf(out, "EMBEDDED RULE SETS (%s total)\n", humanSize(totalRuleSetSize()))
			w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TAG\tKIND\tSIZE\tSOURCE\tDESCRIPTION")
			for _, rs := range rules.RuleSets() {
				size, _ := rules.RuleSetSize(rs.Tag)
				fmt.Fprintf(w, "%s\t%s\t%d B\t%s\t%s\n", rs.Tag, rs.Kind(), size, rs.Source, rs.Description)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "A per-user rules file (to add or override entries) is not implemented yet;")
			fmt.Fprintln(out, "it would slot in where rules.BuiltinRules returns its slice (internal/rules).")
			return nil
		},
	}
}

func shortCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func joinTags(tags []string) string {
	out := ""
	for i, t := range tags {
		if i > 0 {
			out += " "
		}
		out += t
	}
	return out
}

func totalRuleSetSize() int {
	total := 0
	for _, rs := range rules.RuleSets() {
		if n, err := rules.RuleSetSize(rs.Tag); err == nil {
			total += n
		}
	}
	return total
}

func humanSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%.1f KiB (%d bytes)", float64(bytes)/1024, bytes)
}
