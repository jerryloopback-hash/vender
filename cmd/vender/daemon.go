package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
)

func (a *app) newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the foreground daemon that manages sing-box",
		Long: `Run the headless daemon.

The daemon is the authoritative component: it reads config.yaml and
servers.yaml, renders a sing-box configuration into the run directory, starts
and supervises the sing-box child process, and streams its log to standard
output. It runs in the foreground and shuts down on SIGINT or SIGTERM, which
is what a systemd unit or the Windows service wrapper (M4) expects.

Only one daemon may own a run directory: an advisory lock is taken before
sing-box is started, and a second ` + "`vender daemon`" + ` fails immediately
instead of fighting over the inbound port.

  Linux   run dir  $XDG_CACHE_HOME/vender   (default ~/.cache/vender)
  Windows run dir  %LocalAppData%\vender

Use --run-dir to override it (tests, portable installs).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runDaemon(cmd)
		},
	}
}

func (a *app) runDaemon(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	store, err := a.configStore()
	if err != nil {
		return err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return err
	}
	servers, err := store.LoadServers()
	if err != nil {
		return err
	}
	if err := cfg.ValidateAgainst(servers); err != nil {
		return err
	}
	srv, err := resolveSelectedServer(cfg, servers)
	if err != nil {
		return err
	}
	runDir, err := a.runDirPath()
	if err != nil {
		return err
	}

	// One daemon per run dir. The lock is advisory and released by the OS when
	// the process dies, so a crash can never leave a stale lock behind.
	lock, err := core.AcquireLock(core.LockPath(runDir))
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := core.WritePid(runDir); err != nil {
		return err
	}
	defer core.RemovePid(runDir)

	binaryPath, err := core.ResolveBinary(*cfg)
	if err != nil {
		return err
	}

	rc := core.RuntimeConfig{
		Config:     *cfg,
		Server:     srv,
		BinaryPath: binaryPath,
		RunDir:     runDir,
	}
	engine, err := core.New(rc)
	if err != nil {
		return err
	}
	if err := engine.WriteConfig(rc); err != nil {
		return err
	}

	// The signal handler is installed *before* the engine is spawned, so a
	// SIGTERM that arrives during startup is caught and the child is still
	// reaped instead of being orphaned.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(out, "vender daemon %s\n", version)
	fmt.Fprintf(out, "  pid          %d\n", os.Getpid())
	fmt.Fprintf(out, "  run dir      %s\n", runDir)
	fmt.Fprintf(out, "  engine       %s\n", binaryPath)
	fmt.Fprintf(out, "  listen       %s\n", cfg.Proxy.Listen)
	fmt.Fprintf(out, "  mode         %s\n", cfg.Proxy.Mode)
	if srv != nil {
		fmt.Fprintf(out, "  server       %s\n", srv.String())
	} else {
		fmt.Fprintf(out, "  server       (none; direct mode)\n")
	}
	fmt.Fprintln(out, "  stop with Ctrl-C or SIGTERM")

	if err := engine.Start(); err != nil {
		return err
	}
	fmt.Fprintf(out, "listening on %s\n", cfg.Proxy.Listen)

	// Engine output is the daemon's own log: stream it to stdout until the
	// channel closes (the process exited), which also keeps the channel drained
	// so the reporter never blocks.
	//
	// The subscription is taken *after* Start: Logs() returns the channel of
	// the current run, and a run's channel only exists once Start has spawned
	// it. Output emitted before the subscription is buffered, so the listener
	// announcement is not lost.
	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		for line := range engine.Logs() {
			fmt.Fprintln(out, "sing-box:", line)
		}
	}()

	<-ctx.Done()
	fmt.Fprintln(out, "stopping...")
	if err := engine.Stop(); err != nil {
		return err
	}
	// Wait for the log stream to drain so the last line is not lost.
	select {
	case <-logsDone:
	case <-time.After(time.Second):
	}
	fmt.Fprintln(out, "stopped")
	return nil
}

// resolveSelectedServer returns the server named by config.yaml, or nil when
// the mode is direct (direct mode does not need a usable server at all).
func resolveSelectedServer(cfg *config.Config, ss *config.ServerSet) (*config.Server, error) {
	if cfg.Proxy.Mode == config.ModeDirect {
		return nil, nil
	}
	if cfg.SelectedServer == "" {
		return nil, fmt.Errorf("no server selected for proxy mode %q:\n"+
			"  import one with `vender import '<share-link>'`\n"+
			"  or pick one in %s", cfg.Proxy.Mode, "config.yaml")
	}
	srv, ok := ss.Get(cfg.SelectedServer)
	if !ok {
		return nil, fmt.Errorf("%w: %q (import it, or fix selected_server in config.yaml)",
			config.ErrNotFound, cfg.SelectedServer)
	}
	return srv, nil
}

// errNotRunning is reported by `vender status` when no daemon owns the run
// directory. It makes the exit code meaningful for scripts.
var errNotRunning = errors.New("vender daemon is not running")

// daemonRunning reports whether a daemon owns runDir, and its recorded pid.
func daemonRunning(runDir string) (bool, int, error) {
	lock, err := core.AcquireLock(core.LockPath(runDir))
	if err == nil {
		// We got the lock, so nobody holds it.
		_ = lock.Release()
		return false, 0, nil
	}
	if errors.Is(err, core.ErrLocked) {
		return true, core.ReadPid(runDir), nil
	}
	return false, 0, err
}
