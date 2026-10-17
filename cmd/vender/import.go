package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/subscription"
)

// subscriptionClient builds the HTTP client the subscription commands use. It
// is a variable so the CLI tests can substitute a hermetic transport: the
// import tests must never reach the network.
var subscriptionClient = func() *http.Client {
	return &http.Client{Timeout: subscription.DefaultTimeout}
}

func (a *app) newImportCmd() *cobra.Command {
	var (
		name       string
		force      bool
		subURL     string
		profile    string
		namePrefix string
	)

	cmd := &cobra.Command{
		Use:   "import <share-link>",
		Short: "Add servers from a share link or a subscription URL",
		Long: `Parse a share link and append it to servers.yaml, or import a whole
subscription with --url.

Supported share links:

  vless://<uuid>@<host>:<port>?encryption=none&flow=xtls-rprx-vision&
        security=reality&sni=...&fp=chrome&pbk=...&sid=...&type=tcp#name
  hysteria2://<password>@<host>:<port>/?sni=...&obfs=salamander&
        obfs-password=...&insecure=1#name      (hy2:// is accepted too)
  trojan://<password>@<host>:<port>?sni=...&type=ws&path=/ws#name
  vmess://<base64 json>                        (v2rayN form)
  ss://<base64(method:password)>@<host>:<port>#name   (SIP002, and the
  legacy base64(method:password@host:port) form)

The server name comes from the link's #fragment (vmess: the "ps" field). When
no server is selected yet, the first imported server also becomes the active
one.

A subscription (--url) is fetched with a v2ray user agent and may be any of:
base64 of newline-separated share links, plain newline-separated share links,
or Clash/Mihomo YAML with a proxies: list. Every node is added under a profile
(default: the URL's host); see ` + "`vender profiles`" + ` and
` + "`vender profiles use`" + ` to make it active.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(subURL) != "" {
				if len(args) > 0 {
					return errors.New("import: give either a share link or --url, not both")
				}
				return a.importSubscription(cmd, subURL, profile, namePrefix, force)
			}
			if len(args) != 1 {
				return errors.New("import: give a share link, or a subscription with --url <url>")
			}
			return a.importSingleLink(cmd, args[0], name, force)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "override the name taken from the link's #fragment")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing server with the same name")
	cmd.Flags().StringVar(&subURL, "url", "", "import every node of a subscription URL")
	cmd.Flags().StringVar(&profile, "profile", "", "profile to file the subscription under (default: the URL's host)")
	cmd.Flags().StringVar(&namePrefix, "name-prefix", "", "prefix every imported node name (useful to avoid collisions)")
	return cmd
}

// importSingleLink is the M0 path: one share link, filed under the implicit
// manual profile.
func (a *app) importSingleLink(cmd *cobra.Command, link, name string, force bool) error {
	out := cmd.OutOrStdout()

	srv, err := config.ParseShareLink(link)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	if n := strings.TrimSpace(name); n != "" {
		srv.Name = n
	}
	srv.Normalize()
	if err := srv.Validate(); err != nil {
		return fmt.Errorf("import: %w", err)
	}

	store, err := a.configStore()
	if err != nil {
		return err
	}
	ss, err := store.LoadServers()
	if err != nil {
		return err
	}
	_, replaced := ss.Get(srv.Name)
	if err := ss.Add(*srv, force); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	if err := store.SaveServers(ss); err != nil {
		return err
	}

	selected := false
	if cfg, err := store.LoadConfig(); err == nil && cfg.SelectedServer == "" {
		// A single configured server is almost certainly the one the
		// user wants to use, so preselect it.
		cfg.SelectedServer = srv.Name
		if err := store.SaveConfig(cfg); err != nil {
			return fmt.Errorf("import: select %q: %w", srv.Name, err)
		}
		selected = true
	}

	fmt.Fprintf(out, "Imported %s\n", srv)
	if replaced {
		fmt.Fprintf(out, "  replaced the existing entry with the same name\n")
	}
	fmt.Fprintf(out, "  saved to %s (mode 0600)\n", store.ServersPath())
	if selected {
		fmt.Fprintf(out, "  selected as the active server\n")
	}
	fmt.Fprintf(out, "Run `vender servers list` to review what is configured.\n")
	return nil
}

// importSubscription fetches and imports a whole subscription into a new (or
// existing) profile.
func (a *app) importSubscription(cmd *cobra.Command, rawURL, profile, prefix string, force bool) error {
	out := cmd.OutOrStdout()

	store, err := a.configStore()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), subscription.DefaultTimeout+5*time.Second)
	defer cancel()
	client := subscriptionClient()
	fmt.Fprintf(out, "Fetching %s …\n", redactURL(rawURL))
	body, err := subscription.Fetch(ctx, client, rawURL)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	res, format, err := subscription.Parse(body)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}

	profileName := strings.TrimSpace(profile)
	if profileName == "" {
		profileName = subscription.DeriveProfileName(rawURL)
	}
	servers := res.Servers
	if p := strings.TrimSpace(prefix); p != "" {
		for i := range servers {
			servers[i].Name = p + servers[i].Name
		}
	}

	stats, canonical, err := store.ProfileImport(profileName, rawURL, time.Now(), servers, false, force)
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}

	fmt.Fprintf(out, "Subscription format: %s (%d node(s))\n", format, len(res.Servers))
	printImportStats(out, stats)
	printImportSkips(out, res.Skipped)
	reportConflicts(out, stats)
	fmt.Fprintf(out, "Profile %q saved to %s\n", canonical, store.ProfilesPath())
	fmt.Fprintf(out, "  servers in %s (mode 0600)\n", store.ServersPath())
	fmt.Fprintf(out, "\nNext:\n")
	fmt.Fprintf(out, "  vender profiles use %s     # auto failover over this profile\n", canonical)
	fmt.Fprintf(out, "  vender profiles list       # see every profile and its node count\n")
	return nil
}

// printImportStats renders the per-node summary line.
func printImportStats(out interface{ Write([]byte) (int, error) }, stats config.ImportStats) {
	fmt.Fprintf(out, "  added %d, updated %d, unchanged %d", stats.Added, stats.Updated, stats.Unchanged)
	if len(stats.Removed) > 0 {
		fmt.Fprintf(out, ", removed %d", len(stats.Removed))
	}
	if len(stats.Skipped) > 0 {
		fmt.Fprintf(out, ", name collisions %d", len(stats.Skipped))
	}
	fmt.Fprintln(out)
}

// reportConflicts explains nodes that could not be filed because another
// profile already uses their name.
func reportConflicts(out interface{ Write([]byte) (int, error) }, stats config.ImportStats) {
	if len(stats.Skipped) == 0 {
		return
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "\n  name already used by another profile (use --name-prefix or --force):\n")
	for _, c := range stats.Skipped {
		fmt.Fprintf(w, "  - %s\t(held by profile %q)\n", c.Name, c.InProfile)
	}
	_ = w.Flush()
}

// printImportSkips reports nodes the parser could not use, with the reason.
func printImportSkips(out interface{ Write([]byte) (int, error) }, skipped []subscription.Skipped) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(out, "  skipped %d unusable node(s):\n", len(skipped))
	limit := len(skipped)
	if limit > 10 {
		limit = 10
	}
	for _, s := range skipped[:limit] {
		fmt.Fprintf(out, "    - %s: %s\n", s.Node, firstLine(s.Reason))
	}
	if len(skipped) > limit {
		fmt.Fprintf(out, "    … and %d more\n", len(skipped)-limit)
	}
}

// redactURL prints a subscription URL without its userinfo or query, so a
// token-bearing URL never reaches a terminal or a log.
func redactURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if q := strings.Index(rest, "?"); q >= 0 {
			rest = rest[:q] + "?…"
		}
		return raw[:i+3] + rest
	}
	return raw
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
