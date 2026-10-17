package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
)

// newProfilesShowCmd prints one profile in full: its source, its node count and
// — the part that has no one-line form — its DNS policy, which is what decides
// how the profile's node hostnames are resolved.
func (a *app) newProfilesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show [name]",
		Aliases: []string{"get"},
		Short:   "Show a profile in full, including its DNS policy",
		Long: `Print one profile: its node count, subscription URL, last fetch time, and
the DNS policy captured for its node hostnames.

With no name, the active profile is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			ss, err := store.LoadServers()
			if err != nil {
				return err
			}
			ps, err := store.LoadProfiles()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(strings.Join(args, ""))
			if name == "" {
				name = ps.ActiveName()
			}
			canonical, ok := config.KnownProfileName(ss, ps, name)
			if !ok {
				return fmt.Errorf("profiles show: %w: profile %q (see `vender profiles list`)", config.ErrNotFound, name)
			}
			if canonical == config.ProfileAll {
				return fmt.Errorf("profiles show: %q is not a single profile; name one of `vender profiles list`", name)
			}

			active := ps.ActiveName()
			suffix := ""
			if strings.EqualFold(canonical, active) {
				suffix = "  (active)"
			}
			fmt.Fprintf(out, "profile     %s%s\n", canonical, suffix)
			fmt.Fprintf(out, "nodes       %d\n", ss.CountInProfile(canonical))
			if p, ok := ps.Get(canonical); ok {
				if p.URL != "" {
					fmt.Fprintf(out, "url         %s\n", redactURL(p.URL))
				}
				if !p.FetchedAt.IsZero() {
					fmt.Fprintf(out, "updated     %s\n", p.FetchedAt.Format("2006-01-02 15:04:05"))
				}
				printProfileDNSPolicy(out, p.DNS)
				return nil
			}
			fmt.Fprintf(out, "\nNo subscription metadata: %q is known only through servers.yaml.\n", canonical)
			fmt.Fprintf(out, "  set a DNS policy with `vender profiles set-dns %s --domain D --server S`.\n", canonical)
			return nil
		},
	}
}

// printProfileDNSPolicy renders a captured policy in full, including the raw
// source block, so a user can compare it with their provider's documentation.
func printProfileDNSPolicy(out interface{ Write([]byte) (int, error) }, dns *config.ProfileDNS) {
	if dns == nil {
		fmt.Fprintf(out, "\nDNS policy  none — node hostnames use vender's built-in resolver.\n")
		fmt.Fprintf(out, "  declare one with `vender profiles set-dns <name> --domain D --server S`.\n")
		return
	}
	fmt.Fprintf(out, "\nDNS policy for node hostnames:\n")
	if len(dns.NamespacePolicy) == 0 {
		fmt.Fprintf(out, "  no nameserver-policy: node hostnames use the built-in resolver\n")
	}
	for _, e := range dns.NamespacePolicy {
		fmt.Fprintf(out, "  %s -> %s\n", e.Domain, e.Server)
	}
	if len(dns.Nameservers) > 0 {
		fmt.Fprintf(out, "  nameservers (captured, not applied): %s\n", strings.Join(dns.Nameservers, ", "))
	}
	if len(dns.Fallback) > 0 {
		fmt.Fprintf(out, "  fallback (captured, not applied): %s\n", strings.Join(dns.Fallback, ", "))
	}
	if raw := strings.TrimRight(dns.Raw, "\n"); raw != "" {
		fmt.Fprintf(out, "  raw:\n")
		for _, line := range strings.Split(raw, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
}

// newProfilesSetDNSCmd declares or overrides a profile's DNS policy. It exists
// because a provider whose policy is missing or wrong must be fixable by hand
// without editing profiles.yaml, and because the captured policy is the only
// thing that makes some provider's node hostnames resolve at all.
func (a *app) newProfilesSetDNSCmd() *cobra.Command {
	var domains, servers []string
	var clear bool

	cmd := &cobra.Command{
		Use:   "set-dns <name>",
		Short: "Set the profile's DNS policy for its node hostnames",
		Long: `Set (or override) the resolver that answers for a profile's node hostnames.

A provider's node hostnames are often opaque "entry" domains whose public
answers are a decoy. The provider's own Clash config states which resolver to
use; vender captures that at import, and this command declares or fixes it by
hand. Domains are suffix patterns: "v51124-6.qpon" covers that domain and every
subdomain (a leading "+.", "*.", or "." is accepted and ignored).

  vender profiles set-dns provider --domain v51124-6.qpon --server tcp://dns.example:8080

Give --domain and --server in pairs (each may be repeated) to add several rules.
A rule replaces an existing one for the same domain; use --clear to remove the
whole policy and fall back to vender's built-in resolver.

Only the runtime engine is affected: the built-in split DNS that answers the
*client's* queries is untouched. The change takes effect at the next engine
restart; a running daemon is told to restart it here.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if clear && (len(domains) > 0 || len(servers) > 0) {
				return fmt.Errorf("profiles set-dns: --clear cannot be combined with --domain/--server")
			}
			if !clear {
				if len(domains) == 0 {
					return fmt.Errorf("profiles set-dns: give --domain and --server (or --clear)")
				}
				if len(domains) != len(servers) {
					return fmt.Errorf("profiles set-dns: --domain and --server must be given in pairs (%d domain(s), %d server(s))", len(domains), len(servers))
				}
			}

			store, err := a.configStore()
			if err != nil {
				return err
			}
			ss, err := store.LoadServers()
			if err != nil {
				return err
			}
			ps, err := store.LoadProfiles()
			if err != nil {
				return err
			}
			canonical, ok := config.KnownProfileName(ss, ps, args[0])
			if !ok {
				return fmt.Errorf("profiles set-dns: %w: profile %q (see `vender profiles list`)", config.ErrNotFound, args[0])
			}
			if canonical == config.ProfileAll || strings.EqualFold(canonical, config.ProfileManual) {
				return fmt.Errorf("profiles set-dns: %q is not a single imported profile", canonical)
			}

			var dns *config.ProfileDNS
			if !clear {
				if p, ok := ps.Get(canonical); ok {
					dns = p.DNS.Clone()
				}
				if dns == nil {
					dns = &config.ProfileDNS{}
				}
				for i := range domains {
					if err := dns.SetNamespacePolicy(domains[i], servers[i]); err != nil {
						return fmt.Errorf("profiles set-dns: %w", err)
					}
				}
			}

			name, err := store.SetProfileDNS(canonical, dns)
			if err != nil {
				return err
			}
			if clear {
				fmt.Fprintf(out, "Cleared the DNS policy of profile %q; node hostnames use the built-in resolver again.\n", name)
			} else {
				fmt.Fprintf(out, "DNS policy of profile %q saved to %s:\n", name, store.ProfilesPath())
				for _, e := range dns.NamespacePolicy {
					fmt.Fprintf(out, "  %s -> %s\n", e.Domain, e.Server)
				}
			}
			if running, _, _ := daemonRunningFor(a); running {
				if err := a.restartViaDaemon(cmd); err != nil {
					fmt.Fprintf(out, "  warning: could not restart the running daemon: %v\n", err)
				} else {
					fmt.Fprintln(out, "  the running daemon restarted its engine with the new DNS policy")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&domains, "domain", nil, "domain suffix pattern, e.g. v51124-6.qpon (repeatable, paired with --server)")
	cmd.Flags().StringArrayVar(&servers, "server", nil, "resolver for the matching --domain, e.g. tcp://dns.example:8080 (repeatable)")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the profile's DNS policy")
	return cmd
}
