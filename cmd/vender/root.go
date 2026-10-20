package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
)

const appName = "vender"

// app carries per-invocation state shared by all subcommands. It exists so
// tests can point the CLI at a temporary config directory.
type app struct {
	pingProbe core.PingProbe
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
		if home := invokingUserHome(); home != "" {
			return filepath.Join(home, ".cache", appName), nil
		}
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
		// Under sudo, $HOME is root's, but a per-user tool must read the
		// *invoking* user's configuration: `sudo vender autostart on` decides
		// the mechanism from that user's config.yaml and installs a unit whose
		// User= is that user, so reading root's config would both miss TUN mode
		// and register the wrong home.
		if home := invokingUserHome(); home != "" {
			a.store = config.NewStoreAt(filepath.Join(home, ".config", appName))
			return a.store, nil
		}
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

// invokingUserHome returns the home directory of the user who invoked a
// root-owned process through sudo, or "" when this is not such a process. It
// lets `sudo vender …` operate on the user's files instead of root's.
func invokingUserHome() string {
	if os.Geteuid() != 0 {
		return ""
	}
	name := strings.TrimSpace(os.Getenv("SUDO_USER"))
	if name == "" || name == "root" {
		return ""
	}
	u, err := user.Lookup(name)
	if err != nil || u.HomeDir == "" {
		return ""
	}
	return u.HomeDir
}

func newRootCmd() *cobra.Command { return newRootCmdWithApp(&app{}) }

func newRootCmdWithApp(a *app) *cobra.Command {

	root := &cobra.Command{
		Use:     appName,
		Short:   "vender is a lightweight personal proxy client",
		Version: version,
		Long: `vender is a lightweight, cross-platform personal proxy client.

It runs sing-box as a managed child process and exposes a local mixed
SOCKS5+HTTP inbound (default 127.0.0.1:7897) that applications can use.
Supported outbounds are VLESS + Vision + REALITY, Hysteria2, Shadowsocks,
VMess and Trojan; protocol-specific transport limits are documented in README.

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
		a.newProfilesCmd(),
		a.newRulesCmd(),
		a.newDaemonCmd(),
		a.newStatusCmd(),
		a.newProxyCmd(),
		a.newTUICmd(),
		a.newAutostartCmd(),
	)
	return root
}
