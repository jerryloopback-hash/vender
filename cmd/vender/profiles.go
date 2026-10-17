package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/subscription"
)

func (a *app) newProfilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "Manage server profiles (groups of nodes, e.g. from a subscription)",
		Long: `Manage profiles: named groups of servers in servers.yaml.

A profile is how vender tells "the nodes I typed in by hand" apart from "the
nodes my provider gave me". Every server carries a profile name; the built-in
` + "`manual`" + ` profile holds everything imported by hand and always exists.

  vender profiles list             every profile, its node count and source URL
  vender profiles show [name]      one profile in full, including its DNS policy
  vender profiles use <name>       make a profile active
  vender profiles update [name]    re-fetch a profile's subscription
  vender profiles set-dns <name>   set/override the profile's DNS policy
  vender profiles rm <name>        remove a profile and its servers

What "active" means: with ` + "`selected_server: auto`" + `, the engine runs a URLTest
failover group over the *active profile's* servers only, so switching profiles
switches which nodes it may use. Naming a specific server with
` + "`vender servers use <name>`" + ` still works and implies nothing about the
profile. Use ` + "`vender profiles use all`" + ` to fail over over every server.

The DNS policy is separate from the client-facing split DNS: it tells the
engine which resolver answers for the *profile's node hostnames* (a provider's
entry domains are resolved by the provider's own DNS, not public DNS). It is
captured at import and overridable with ` + "`profiles set-dns`" + `.
`,
	}
	cmd.AddCommand(
		a.newProfilesListCmd(),
		a.newProfilesShowCmd(),
		a.newProfilesUseCmd(),
		a.newProfilesUpdateCmd(),
		a.newProfilesSetDNSCmd(),
		a.newProfilesRmCmd(),
	)
	return cmd
}

func (a *app) newProfilesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List profiles, node counts and subscription URLs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			active := ps.ActiveName()

			names := config.KnownProfiles(ss, ps)
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ACTIVE\tPROFILE\tNODES\tDNS\tUPDATED\tSOURCE URL")
			for _, name := range names {
				mark := "  "
				if strings.EqualFold(name, active) {
					mark = "*"
				}
				url, updated, dns := "", "", "-"
				if p, ok := ps.Get(name); ok {
					url = redactURL(p.URL)
					if !p.FetchedAt.IsZero() {
						updated = p.FetchedAt.Format("2006-01-02 15:04")
					}
					dns = dnsSummary(p.DNS)
				}
				note := ""
				if strings.EqualFold(name, config.ProfileManual) {
					note = "(built-in)"
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", mark, name, ss.CountInProfile(name), dns, updated, firstNonEmptyStr(url, note))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nactive profile: %s\n", active)
			if strings.EqualFold(active, config.ProfileManual) {
				fmt.Fprintf(out, "  `selected_server: auto` fails over over the %d server(s) imported by hand.\n", ss.CountInProfile(config.ProfileManual))
				if len(names) > 1 {
					fmt.Fprintf(out, "  switch to an imported profile with `vender profiles use <name>`.\n")
				}
			} else {
				fmt.Fprintf(out, "  `selected_server: auto` fails over over the %d server(s) of profile %q.\n",
					ss.CountInProfile(active), active)
			}
			return nil
		},
	}
}

func (a *app) newProfilesUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "use <name|all>",
		Aliases: []string{"select", "switch"},
		Short:   "Make a profile active (auto failover scopes to it)",
		Long: `Set the active profile in profiles.yaml.

With ` + "`selected_server: auto`" + `, vender renders a URLTest group over the active
profile's servers only. Use ` + "`all`" + ` to fail over over every server.

The change takes effect at the next engine restart; a running daemon is told to
restart it if one is attached.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			active, err := store.SelectProfile(args[0])
			if err != nil {
				return err
			}
			ss, err := store.LoadServers()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "active profile = %s (%d server(s))\n", active, ss.CountInProfile(active))
			fmt.Fprintf(out, "  `selected_server: auto` now fails over over those servers only.\n")
			if running, _, _ := daemonRunningFor(a); running {
				if err := a.restartViaDaemon(cmd); err != nil {
					fmt.Fprintf(out, "  warning: could not restart the running daemon: %v\n", err)
				} else {
					fmt.Fprintln(out, "  the running daemon restarted its engine with the new profile")
				}
			}
			return nil
		},
	}
}

func (a *app) newProfilesRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a profile and its servers",
		Long: `Delete a profile: its servers, its registry entry, and the active
selection if it was the active profile.

The built-in ` + "`manual`" + ` profile cannot be removed, so there is always at least
one profile left. If config.yaml selected a server this deletes, the selection
falls back to ` + "`auto`" + ` so the daemon can still start.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			before, err := store.LoadProfiles()
			if err != nil {
				return err
			}
			wasActive := strings.EqualFold(before.ActiveName(), args[0])
			removed, err := store.RemoveProfile(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed profile %q and %d server(s)\n", args[0], removed)
			if wasActive {
				fmt.Fprintf(out, "  it was the active profile; the active profile is now %q\n", config.ProfileManual)
			}
			if running, _, _ := daemonRunningFor(a); running {
				if err := a.restartViaDaemon(cmd); err != nil {
					fmt.Fprintf(out, "  warning: could not restart the running daemon: %v\n", err)
				} else {
					fmt.Fprintln(out, "  the running daemon restarted its engine")
				}
			}
			return nil
		},
	}
}

func (a *app) newProfilesUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update [name]",
		Short: "Re-fetch a profile's subscription and reconcile its servers",
		Long: `Fetch the profile's subscription again and reconcile it: new nodes are
added, changed nodes are updated and nodes the subscription no longer publishes
are removed. A server selected in config.yaml that disappears falls back to
` + "`auto`" + `.

With no name, the active profile is updated. The profile must have been created
by ` + "`vender import --url`" + ` (a hand-made profile has no URL to fetch).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
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
			if strings.EqualFold(name, config.ProfileManual) {
				return fmt.Errorf("profiles update: %q is the built-in profile and has no subscription; name one created by `vender import --url`", config.ProfileManual)
			}
			prof, ok := ps.Get(name)
			if !ok || strings.TrimSpace(prof.URL) == "" {
				return fmt.Errorf("profiles update: profile %q has no subscription URL recorded (import one with `vender import --url <url> --profile %s`)", name, name)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), subscription.DefaultTimeout+5*time.Second)
			defer cancel()
			client := subscriptionClient()
			fmt.Fprintf(out, "Fetching %s …\n", redactURL(prof.URL))
			body, err := subscription.Fetch(ctx, client, prof.URL)
			if err != nil {
				return fmt.Errorf("profiles update: %w", err)
			}
			res, format, err := subscription.Parse(body)
			if err != nil {
				return fmt.Errorf("profiles update: %w", err)
			}

			stats, canonical, err := store.ProfileImport(prof.Name, prof.URL, time.Now(), res.Servers, res.DNS, true, false)
			if err != nil {
				return fmt.Errorf("profiles update: %w", err)
			}
			fmt.Fprintf(out, "Profile %q updated from %s subscription (%d node(s))\n", canonical, format, len(res.Servers))
			printImportStats(out, stats)
			printImportSkips(out, res.Skipped)
			reportConflicts(out, stats)
			printRemoved(out, stats.Removed)
			printProfileDNS(out, res.DNS)
			if n, serr := a.keepSelectionSane(store); serr != nil {
				fmt.Fprintf(out, "  warning: %v\n", serr)
			} else if n != "" {
				fmt.Fprintf(out, "  the selected server %q is gone; selection fell back to auto\n", n)
			}
			if running, _, _ := daemonRunningFor(a); running {
				if err := a.restartViaDaemon(cmd); err != nil {
					fmt.Fprintf(out, "  warning: could not restart the running daemon: %v\n", err)
				} else {
					fmt.Fprintln(out, "  the running daemon restarted its engine with the reconciled list")
				}
			}
			return nil
		},
	}
}

// keepSelectionSane clears a selected_server that no longer exists, so a
// refresh cannot leave config.yaml pointing at a deleted node. It returns the
// name that was dropped, if any.
func (a *app) keepSelectionSane(store *config.Store) (string, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return "", err
	}
	if cfg.SelectedServer == "" || cfg.IsAutoServer() {
		return "", nil
	}
	ss, err := store.LoadServers()
	if err != nil {
		return "", err
	}
	if _, ok := ss.Get(cfg.SelectedServer); ok {
		return "", nil
	}
	dropped := cfg.SelectedServer
	cfg.SelectedServer = config.SelectedServerAuto
	if err := store.SaveConfig(cfg); err != nil {
		return "", err
	}
	return dropped, nil
}

func printRemoved(out interface{ Write([]byte) (int, error) }, removed []string) {
	if len(removed) == 0 {
		return
	}
	sorted := append([]string(nil), removed...)
	sort.Strings(sorted)
	fmt.Fprintf(out, "  removed %d node(s) the subscription no longer publishes:\n", len(sorted))
	for _, n := range sorted {
		fmt.Fprintf(out, "    - %s\n", n)
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// dnsSummary is the one-column `profiles list` view of a profile's DNS policy.
func dnsSummary(d *config.ProfileDNS) string {
	if !d.Applied() {
		return "-"
	}
	if n := len(d.NamespacePolicy); n == 1 {
		return "1 rule"
	} else {
		return fmt.Sprintf("%d rules", n)
	}
}
