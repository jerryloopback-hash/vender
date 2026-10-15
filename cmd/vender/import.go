package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
)

func (a *app) newImportCmd() *cobra.Command {
	var (
		name  string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "import <share-link>",
		Short: "Add a server from a vless:// or hysteria2:// share link",
		Long: `Parse a share link and append it to servers.yaml.

Supported links:

  vless://<uuid>@<host>:<port>?encryption=none&flow=xtls-rprx-vision&
        security=reality&sni=...&fp=chrome&pbk=...&sid=...&type=tcp#name
  hysteria2://<password>@<host>:<port>/?sni=...&obfs=salamander&
        obfs-password=...&insecure=1#name
  hy2://<password>@<host>:<port>/?...#name

The server name comes from the link's #fragment. When no server is selected
yet, the first imported server also becomes the active one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			srv, err := config.ParseShareLink(args[0])
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
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "override the name taken from the link's #fragment")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing server with the same name")
	return cmd
}
