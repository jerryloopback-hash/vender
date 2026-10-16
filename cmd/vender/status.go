package main

import (
	"fmt"
	"net"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
)

func (a *app) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon and the local proxy are up",
		Long: `Print the daemon's state, and exit non-zero when the proxy is not usable.

"Running" is decided by the advisory lock in the run directory, not by a pid
file: the lock is released by the operating system even if the daemon is
killed, so it cannot go stale. The pid file is only used to display the pid.

The exit code is 0 when a daemon is running and its local inbound accepts
connections, and non-zero otherwise, which makes the command usable in a script
or an autostart health check.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runStatus(cmd)
		},
	}
}

func (a *app) runStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	store, err := a.configStore()
	if err != nil {
		return err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	runDir, err := a.runDirPath()
	if err != nil {
		return err
	}

	running, pid, err := daemonRunning(runDir)
	if err != nil {
		return err
	}
	listening, dialErr := portAccepts(cfg.Proxy.Listen)

	fmt.Fprintf(out, "state        %s\n", stateLabel(running))
	fmt.Fprintf(out, "pid          %s\n", pidLabel(pid))
	fmt.Fprintf(out, "run dir      %s\n", runDir)
	fmt.Fprintf(out, "listen       %s (%s)\n", cfg.Proxy.Listen, listenLabel(listening))
	fmt.Fprintf(out, "mode         %s\n", cfg.Proxy.Mode)
	fmt.Fprintf(out, "tun          %s\n", tunLabel(cfg))
	fmt.Fprintf(out, "server       %s\n", selectedServerLabel(cfg, store))

	switch {
	case !running:
		return fmt.Errorf("%w (nothing holds %s); start it with `vender daemon`",
			errNotRunning, runDir)
	case !listening:
		return fmt.Errorf("vender daemon (pid %s) is running but %s is not accepting connections: %v",
			pidLabel(pid), cfg.Proxy.Listen, dialErr)
	default:
		return nil
	}
}

func stateLabel(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}

func listenLabel(listening bool) string {
	if listening {
		return "accepting connections"
	}
	return "not accepting connections"
}

func pidLabel(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", pid)
}

// selectedServerLabel renders the active server from config.yaml without
// failing when it is missing: status must describe reality even when the
// configuration is broken.
func selectedServerLabel(cfg *config.Config, store *config.Store) string {
	if cfg.Proxy.Mode == config.ModeDirect {
		return "(none; direct mode)"
	}
	if cfg.SelectedServer == "" {
		return "(none selected)"
	}
	ss, err := store.LoadServers()
	if err != nil {
		return cfg.SelectedServer + " (servers.yaml unreadable: " + err.Error() + ")"
	}
	srv, ok := ss.Get(cfg.SelectedServer)
	if !ok {
		return cfg.SelectedServer + " (missing from servers.yaml)"
	}
	return srv.String()
}

// tunLabel describes the TUN inbound for `vender status`: one short line, and
// honest about a TUN that is switched on but cannot start here.
func tunLabel(cfg *config.Config) string {
	t := cfg.Engine.Tun
	if !t.Enabled {
		return "off"
	}
	label := fmt.Sprintf("on (%s, stack %s, auto_route %s)", t.InterfaceName, t.Stack, onOffWord(t.AutoRouteEnabled()))
	// Resolve the engine so a `setcap`-granted binary counts as privileged:
	// the point of the line is to answer "will this actually start?".
	engine, _ := core.ResolveBinary(*cfg)
	if p := core.CheckTUNPrivilege(engine); !p.OK {
		label += " — cannot start: " + p.Detail
	}
	return label
}

// portAccepts reports whether addr accepts a TCP connection right now.
func portAccepts(addr string) (bool, error) {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false, err
	}
	conn.Close()
	return true, nil
}
