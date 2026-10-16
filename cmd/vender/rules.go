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
		Short: "Inspect and edit the routing rules",
		Long: `Show the routing policy vender applies in proxy.mode: rule.

The policy is built in and fully offline: the rule sets it matches against are
compiled sing-box rule sets embedded in the vender binary, so no download and
no data directory is needed at runtime. On top of that:

  * a tiny built-in "critical" list (opencode.ai, api.tavily.com) is evaluated
    first, so a dead proxy can never cut off the machine's control plane;
  * an optional user file, rules.yaml, is merged *before* the built-in policy,
    so a user entry always wins.

  vender rules list    the ordered merged policy, the DNS policy and the sets
  vender rules path    print the path of the user rules file
  vender rules init    write a commented rules.yaml if none exists`,
	}
	cmd.AddCommand(a.newRulesListCmd(), a.newRulesPathCmd(), a.newRulesInitCmd())
	return cmd
}

func (a *app) newRulesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the ordered merged routing policy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			user, err := rules.LoadUserRules(store.RulesPath())
			if err != nil {
				return err
			}
			if user == nil {
				fmt.Fprintf(out, "# user rules: none (%s does not exist; create one with `vender rules init`)\n", store.RulesPath())
			} else {
				fmt.Fprintf(out, "# user rules: %s\n", store.RulesPath())
			}

			fmt.Fprintf(out, "# vender routing policy (proxy.mode: rule)\n")
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

			policy, err := rules.EffectiveRules(user)
			if err != nil {
				return err
			}
			w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "#\tNAME\tMATCHES\tOUTBOUND")
			n := 0
			for _, r := range policy {
				n++
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", n, r.Name, r.Summary(), r.Outbound)
			}
			fmt.Fprintf(w, "\tfinal\t(anything left)\t%s\n", rules.OutProxy)
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)

			dns := rules.RuleModeDNS(user)
			fmt.Fprintln(out, "DNS")
			w = tabwriter.NewWriter(out, 4, 4, 2, ' ', 0)
			for _, s := range dns.Servers {
				where := "direct"
				if s.Detour != "" {
					where = "via " + s.Detour
				}
				addr := s.Server
				if s.Type == "fakeip" {
					addr = s.Inet4Range
				}
				fmt.Fprintf(w, "  server\t%s\t%s %s\t(%s)\n", s.Tag, s.Type, addr, where)
			}
			for _, r := range dns.Rules {
				fmt.Fprintf(w, "  rule\t%s\t%s\t-> %s\n", r.Name, dnsRuleMatch(r), r.Server)
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
			fmt.Fprintln(out, "The critical list and the user file are shown in the merged policy above; a user")
			fmt.Fprintln(out, "entry always wins over the built-in lists, and the critical list wins over both.")
			return nil
		},
	}
}

func (a *app) newRulesPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path of the user rules file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := a.configStore()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), store.RulesPath())
			return nil
		},
	}
}

func (a *app) newRulesInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented rules.yaml if none exists",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			wrote, err := rules.WriteUserRules(store.RulesPath(), force)
			if err != nil {
				return err
			}
			path := store.RulesPath()
			if wrote {
				fmt.Fprintf(out, "Wrote %s\n", path)
				fmt.Fprintln(out, "  the file is empty (all lists are []): add entries and restart the daemon,")
				fmt.Fprintln(out, "  or press R in the TUI, to apply them")
				return nil
			}
			fmt.Fprintf(out, "%s already exists; left unchanged (use --force to overwrite)\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing rules.yaml")
	return cmd
}

// dnsRuleMatch renders a DNS rule's matchers for `vender rules list`.
func dnsRuleMatch(r rules.DNSRule) string {
	var parts []string
	if len(r.RuleSets) > 0 {
		parts = append(parts, "rule_set=["+joinTags(r.RuleSets)+"]")
	}
	if len(r.Domains) > 0 {
		parts = append(parts, "domain=["+joinTags(r.Domains)+"]")
	}
	if len(r.DomainSuffixes) > 0 {
		parts = append(parts, "domain_suffix=["+joinTags(r.DomainSuffixes)+"]")
	}
	if len(r.QueryTypes) > 0 {
		parts = append(parts, "query_type=["+joinTags(r.QueryTypes)+"]")
	}
	return joinTags(parts)
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
