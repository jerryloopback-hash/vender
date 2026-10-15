package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func (a *app) newServersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "Inspect and edit the configured servers",
		Long: `Manage servers.yaml, the list of outbound servers.

  vender servers list            table of configured servers
  vender servers list --links    print the share link of every server
  vender servers rm <name>       delete a server`,
	}
	cmd.AddCommand(a.newServersListCmd(), a.newServersRmCmd())
	return cmd
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
				if strings.EqualFold(cfg.SelectedServer, srv.Name) {
					active = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					active, srv.Name, srv.Type, srv.Endpoint(), srv.Detail())
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if cfg.SelectedServer == "" {
				fmt.Fprintf(out, "\nNo active server selected.\n")
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
