package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
)

func (a *app) newServersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "Inspect and edit the configured servers",
		Long: `Manage servers.yaml, the list of outbound servers.

  vender servers list            table of configured servers
  vender servers list --links    print the share link of every server
  vender servers use <name>      make a server active (or auto for failover)
  vender servers rm <name>       delete a server`,
	}
	cmd.AddCommand(a.newServersListCmd(), a.newServersUseCmd(), a.newServersRmCmd())
	return cmd
}

func (a *app) newServersUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "use <name|auto>",
		Aliases: []string{"select", "switch"},
		Short:   "Make a server active, or turn on automatic failover",
		Long: `Set config.yaml's selected_server.

With a server name, that server is used for every proxied connection. With
` + "`auto`" + `, vender renders a URLTest group over every configured server and lets the
engine pick the fastest reachable one, so a degraded node is bypassed.

The change takes effect at the next engine restart; a running daemon is told to
restart it if one is attached.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			if err := store.SelectServer(name); err != nil {
				return err
			}
			if strings.EqualFold(name, config.SelectedServerAuto) {
				fmt.Fprintf(out, "selected_server = auto (failover over every configured server)\n")
			} else {
				fmt.Fprintf(out, "selected_server = %s\n", name)
			}
			if running, _, _ := daemonRunningFor(a); running {
				if err := a.restartViaDaemon(cmd); err != nil {
					fmt.Fprintf(out, "  warning: could not restart the running daemon: %v\n", err)
					fmt.Fprintln(out, "  the change applies once you restart it (R in the TUI, or Ctrl-C and `vender daemon`)")
				} else {
					fmt.Fprintln(out, "  the running daemon restarted its engine with the new selection")
				}
			}
			return nil
		},
	}
}

// restartViaDaemon asks a running daemon to re-read the config and restart.
func (a *app) restartViaDaemon(cmd *cobra.Command) error {
	runDir, err := a.runDirPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	cli, err := control.Dial(ctx, runDir)
	if err != nil {
		return err
	}
	defer cli.Close()
	if _, err := cli.RestartEngine(ctx); err != nil {
		return err
	}
	return nil
}

func (a *app) newServersListCmd() *cobra.Command {
	var links bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the configured servers",
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

			cfg, err := store.LoadConfig()
			if err != nil {
				return err
			}

			if ss.Len() == 0 {
				fmt.Fprintf(out, "No servers configured (in %s).\n", store.ServersPath())
				fmt.Fprintf(out, "Add one with `vender import '<share-link>'`.\n")
				return nil
			}

			if links {
				fmt.Fprintf(out, "# Share links for %d server(s) in %s\n", ss.Len(), store.ServersPath())
				fmt.Fprintf(out, "# WARNING: these contain credentials.\n")
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				for i := range ss.Servers {
					srv := &ss.Servers[i]
					fmt.Fprintf(w, "%s\t%s\n", srv.Name, srv.ShareLink())
				}
				return w.Flush()
			}

			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ACTIVE\tNAME\tTYPE\tENDPOINT\tDETAIL")
			for i := range ss.Servers {
				srv := &ss.Servers[i]
				active := "  "
				if !cfg.IsAutoServer() && strings.EqualFold(cfg.SelectedServer, srv.Name) {
					active = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					active, srv.Name, srv.Type, srv.Endpoint(), srv.Detail())
			}
			if err := w.Flush(); err != nil {
				return err
			}
			switch {
			case cfg.IsAutoServer():
				fmt.Fprintf(out, "\nselected_server = auto (failover over %d servers)\n", ss.Len())
			case cfg.SelectedServer == "":
				fmt.Fprintf(out, "\nNo active server selected (`vender servers use <name|auto>`).\n")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&links, "links", false, "print the share link of every server (contains credentials)")
	return cmd
}

func (a *app) newServersRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a server",
		Long: `Delete a server from servers.yaml by name (case-insensitive).

If the server was the active one, the selection is cleared.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := a.configStore()
			if err != nil {
				return err
			}
			cfgBefore, err := store.LoadConfig()
			if err != nil {
				return err
			}
			removed, err := store.RemoveServer(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed %s\n", removed)
			if strings.EqualFold(cfgBefore.SelectedServer, removed.Name) {
				fmt.Fprintf(out, "  it was the active server; no server is selected now\n")
			}
			return nil
		},
	}
}
