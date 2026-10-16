package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
)

const appName = "vender"

// app carries per-invocation state shared by all subcommands. It exists so
// tests can point the CLI at a temporary config directory.
type app struct {
	// configDir overrides the platform config directory when non-empty
	// (set by --config-dir).
	configDir string
	// runDir overrides the platform runtime directory when non-empty
	// (set by --run-dir). It is where singbox.json, the lock and the pid file
	// live.
	runDir string
	// store caches the resolved store.
	store *config.Store
}

// runDirPath returns the runtime directory: --run-dir when given, otherwise
// the platform default (os.UserCacheDir()/vender).
func (a *app) runDirPath() (string, error) {
	if a.runDir == "" {
		return core.DefaultRunDir()
	}
	abs, err := filepath.Abs(a.runDir)
	if err != nil {
		return "", fmt.Errorf("resolve --run-dir: %w", err)
	}
	return abs, nil
}

// configStore returns the configuration store for this invocation.
func (a *app) configStore() (*config.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	if a.configDir == "" {
		st, err := config.NewStore()
		if err != nil {
			return nil, err
		}
		a.store = st
		return st, nil
	}
	abs, err := filepath.Abs(a.configDir)
	if err != nil {
		return nil, fmt.Errorf("resolve --config-dir: %w", err)
	}
	a.store = config.NewStoreAt(abs)
	return a.store, nil
}

func newRootCmd() *cobra.Command {
	a := &app{}

	root := &cobra.Command{
		Use:     appName,
		Short:   "vender is a lightweight personal proxy client",
		Version: version,
		Long: `vender is a lightweight, cross-platform personal proxy client.

It runs sing-box as a managed child process and exposes a local mixed
SOCKS5+HTTP inbound (default 127.0.0.1:7897) that applications can use. Two
outbound protocols are supported: VLESS + Vision + REALITY (TCP) and
Hysteria2 (UDP).

The daemon does the proxying; the TUI (M3) is an optional client that attaches
to it. Configuration lives in the platform user config directory:

  Linux    ~/.config/vender/     (or $XDG_CONFIG_HOME/vender)
  Windows  %AppData%\vender\

  config.yaml    global settings
  servers.yaml   server list (contains credentials, mode 0600)

Runtime state (the generated sing-box config, the lock and the pid file) lives
in the platform cache directory instead:

  Linux    ~/.cache/vender/      (or $XDG_CACHE_HOME/vender)
  Windows  %LocalAppData%\vender\`,
		// Errors are self-explanatory; printing the full usage after each one
		// buries them.
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&a.configDir, "config-dir", "",
		"use this directory instead of the platform config directory")
	root.PersistentFlags().StringVar(&a.runDir, "run-dir", "",
		"use this directory instead of the platform runtime directory")

	root.AddCommand(
		a.newTunCmd(),
		a.newVersionCmd(),
		a.newImportCmd(),
		a.newServersCmd(),
		a.newRulesCmd(),
		a.newDaemonCmd(),
		a.newStatusCmd(),
		a.newProxyCmd(),
		a.newTUICmd(),
		a.newAutostartCmd(),
	)
	return root
}
