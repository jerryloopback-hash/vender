package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
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

It also serves the local control channel the TUI attaches to: a loopback TCP
listener with a random token, published as control.json (mode 0600) inside the
run directory. The token is removed on exit, so a stale token can never
outlive its daemon.

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
	// The daemon writes to its stdout from two goroutines — the banner and the
	// engine log pump — so every write is serialised through one writer.
	// Without it two lines can interleave, and (as the race detector notices)
	// an unbuffered writer like the one the tests use is not safe for it.
	out := &syncWriter{w: cmd.OutOrStdout()}

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

	// The controller owns the engine from here on: it renders the runtime
	// configuration (including sing-box's Clash API, which is how live traffic
	// and latency reach the TUI), restarts it when the server or mode changes,
	// and fans engine output out to every attached client.
	ctrl, err := control.NewController(control.ControllerOptions{
		Store:      store,
		RunDir:     runDir,
		Engine:     engine,
		BinaryPath: binaryPath,
		Version:    version,
		Logf:       func(format string, args ...any) { fmt.Fprintf(out, "vender: "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}
	defer ctrl.Close()

	// Subscribe before starting so the daemon's own stdout carries the very
	// first engine lines; the controller buffers history, so this is a
	// convenience rather than a requirement.
	logs, unsubscribe, err := ctrl.Subscribe(control.StreamLogs)
	if err != nil {
		return err
	}
	defer unsubscribe()

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
	if cfg.Engine.Tun.Enabled {
		tun := cfg.Engine.Tun
		auto := "off"
		if tun.AutoRouteEnabled() {
			auto = "on"
		}
		fmt.Fprintf(out, "  tun          %s (stack %s, mtu %s, auto_route %s, strict_route %s)\n",
			tun.InterfaceName, tun.Stack, mtuDesc(tun.MTU), auto, boolDesc(tun.StrictRoute))
		if p := core.CheckTUNPrivilege(binaryPath); !p.OK {
			fmt.Fprintf(out, "  tun warning  %s\n", p.Detail)
		}
	} else {
		fmt.Fprintf(out, "  tun          off (enable with `vender tun on`)\n")
	}
	if srv != nil {
		fmt.Fprintf(out, "  server       %s\n", srv.String())
	} else {
		fmt.Fprintf(out, "  server       (none; direct mode)\n")
	}
	fmt.Fprintln(out, "  stop with Ctrl-C or SIGTERM")

	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		for v := range logs {
			line, ok := v.(control.LogLine)
			if !ok {
				continue
			}
			if line.Source == control.SourceDaemon {
				fmt.Fprintf(out, "vender: %s\n", line.Line)
				continue
			}
			fmt.Fprintln(out, "sing-box:", line.Line)
		}
	}()

	if err := ctrl.StartEngine(); err != nil {
		return err
	}

	// The control channel is published only once the engine is up: a daemon
	// that failed to start leaves no endpoint behind for a client to dial.
	ctrlSrv, err := control.NewServer(control.ServerOptions{
		Handler: ctrl,
		RunDir:  runDir,
		Version: version,
	})
	if err != nil {
		return err
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		ctrlSrv.Serve()
	}()

	fmt.Fprintf(out, "listening on %s\n", cfg.Proxy.Listen)
	fmt.Fprintf(out, "control on %s (attach with `vender tui`)\n", ctrlSrv.Addr())

	<-ctx.Done()
	fmt.Fprintln(out, "stopping...")

	// Stop accepting clients first: it drops the live subscriptions, so the
	// TUI's streams end cleanly instead of hanging while the engine stops.
	_ = ctrlSrv.Close()
	<-served
	ctrl.Close()

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

// syncWriter serialises writes from the daemon's several output goroutines.
// fmt writes a complete formatted string with a single Write call, so a
// mutex around Write is enough to keep whole lines intact.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

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

// mtuDesc renders the TUN MTU the way the CLI does elsewhere: 0 means the
// engine picks.
func mtuDesc(mtu int) string {
	if mtu == 0 {
		return "auto"
	}
	return fmt.Sprintf("%d", mtu)
}

func boolDesc(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
