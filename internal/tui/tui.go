// Package tui implements vender's terminal user interface: a Bubble Tea client
// that attaches to a running daemon over the control channel and detaches
// freely, leaving the daemon authoritative.
//
// # What it is
//
// Four screens — Dashboard, Nodes, Logs, Settings — with a live, animated view
// of the engine: status, the selected server and mode, an up/down traffic
// graph fed by sing-box's Clash API, latency, and the engine's own log stream.
//
// # What it is not
//
// It never launches or supervises sing-box. Everything it changes it changes by
// asking the daemon (internal/control), which re-renders the engine
// configuration and restarts the child in place. That is why the TUI can be
// started, stopped and restarted at any time, and why two of them can be open
// at once without fighting.
//
// # With no daemon
//
// The TUI starts anyway. Nodes and Settings are built from the configuration
// files on disk (internal/config, internal/rules), so they are fully useful
// with nothing running; the Dashboard says what is missing and prints the exact
// command that fixes it. Attaching is retried once a second, so starting the
// daemon later "just works".
//
// # Terminals
//
// Colour is optional: NO_COLOR, and a stdout that is not a terminal, both fall
// back to plain text (see newTheme). With no terminal at all, Run prints a
// one-shot status snapshot instead of failing. Animation and interactivity
// need a real terminal (a TTY on both stdin and stdout).
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ll-lite/vender/internal/autostart"
	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
	"github.com/ll-lite/vender/internal/core"
)

// Options configures one TUI session.
type Options struct {
	// ConfigDir is the vender configuration directory holding config.yaml and
	// servers.yaml. The TUI reads it directly so its Nodes and Settings screens
	// work with no daemon.
	ConfigDir string
	// RunDir is the daemon's runtime directory, i.e. where the daemon publishes
	// control.json. Empty means the platform default (os.UserCacheDir()/vender),
	// which is also the default of `vender daemon`.
	RunDir string
	// ControlSocket pins the control endpoint ("host:port") instead of reading
	// it from <RunDir>/control.json. The token is still taken from the run dir,
	// because a token in a flag or an environment variable would leak into the
	// process table. Empty means "discover both from the run dir".
	ControlSocket string
	// RefreshInterval is how often the TUI polls daemon status, in seconds.
	// Zero means one second. It does not affect the animation frame rate.
	RefreshInterval int
}

// Run starts the interactive TUI and blocks until the user quits, ctx is
// cancelled, or the terminal goes away. It never requires a daemon to be up.
func Run(ctx context.Context, opts Options) error {
	if opts.ConfigDir == "" {
		if dir, err := config.DefaultDir(); err == nil {
			opts.ConfigDir = dir
		}
	}
	if opts.RunDir == "" {
		dir, err := core.DefaultRunDir()
		if err != nil {
			return err
		}
		opts.RunDir = dir
	}

	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return runPlain(ctx, opts, os.Stdout)
	}

	m := newModel(ctx, opts, newTheme(rendererFor(os.Stdout)))
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	_, err := p.Run()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, tea.ErrProgramKilled), errors.Is(err, tea.ErrInterrupted):
		// A signal is a normal way for a terminal program to end. Bubble Tea
		// has already restored the terminal; exiting 0 makes `vender tui` look
		// the way `q` does.
		return nil
	default:
		return fmt.Errorf("tui: %w", err)
	}
}

// runPlain is the non-terminal fallback: one snapshot, no escape sequences.
// It is what `vender tui | cat`, a CI run, or a Windows console without a TTY
// gets, and it never fails merely because no daemon is running.
func runPlain(ctx context.Context, opts Options, w io.Writer) error {
	fmt.Fprintln(w, "vender tui: no terminal on stdin/stdout — printing one snapshot instead")
	fmt.Fprintf(w, "config dir   %s\n", opts.ConfigDir)
	fmt.Fprintf(w, "run dir      %s\n", opts.RunDir)

	snap := loadSnapshot(opts)
	fmt.Fprintf(w, "listen       %s\n", orDash(snap.Listen))
	fmt.Fprintf(w, "mode         %s\n", orDash(snap.Mode))
	fmt.Fprintf(w, "profile      %s\n", orDash(activeProfileOf(snap)))
	fmt.Fprintf(w, "tun          %s (stack %s, device %s, auto_route %s)\n",
		onOffWord(snap.Tun.Enabled), orDash(snap.Tun.Stack), orDash(snap.Tun.InterfaceName),
		onOffWord(snap.Tun.AutoRouteEnabled()))
	if snap.Tun.Enabled && !snap.TunPriv.OK {
		fmt.Fprintf(w, "             cannot start here: %s\n", snap.TunPriv.Detail)
	}
	fmt.Fprintf(w, "servers      %d configured\n", len(snap.Servers))
	if st, err := autostart.Status(); err == nil {
		fmt.Fprintf(w, "autostart    %s (%s)\n", enabledWord(st.Enabled), st.Mechanism)
	} else {
		fmt.Fprintf(w, "autostart    unknown (%v)\n", err)
	}

	cctx, cancel := context.WithTimeout(ctx, 5*1e9)
	defer cancel()
	cli, err := dial(cctx, opts)
	if err != nil {
		fmt.Fprintf(w, "daemon       not running (%v)\n", err)
		fmt.Fprintln(w, "             start it with `vender daemon`")
		return nil
	}
	defer cli.Close()
	st, err := cli.Status(cctx)
	if err != nil {
		fmt.Fprintf(w, "daemon       unreachable (%v)\n", err)
		return nil
	}
	fmt.Fprintf(w, "daemon       running (pid %d, v%s)\n", st.PID, st.Vender)
	fmt.Fprintf(w, "engine       running=%v healthy=%v pid=%d uptime=%s\n",
		st.EngineRunning, st.EngineHealthy, st.EnginePid, humanDuration(st.UptimeSecs))
	fmt.Fprintf(w, "selected     %s\n", orDash(st.Selected))
	return nil
}

// activeProfileOf is the snapshot's active profile, for the plain (non-TTY)
// snapshot.
func activeProfileOf(snap configSnapshot) string {
	if snap.Profiles == nil {
		return config.ProfileManual
	}
	return snap.Profiles.ActiveName()
}

// rendererFor picks the lipgloss renderer for a writer. A non-terminal or
// NO_COLOR writer gets a plain (ASCII) profile, which strips every escape
// sequence — the same path the view tests exercise.
func rendererFor(w io.Writer) *lipgloss.Renderer {
	if os.Getenv("NO_COLOR") != "" {
		return lipgloss.NewRenderer(io.Discard)
	}
	return lipgloss.NewRenderer(w)
}

// dial attaches to the daemon. It is deliberately not a method on Model: the
// model is pure state, and every command that needs the daemon gets the
// resolved Options.
func dial(ctx context.Context, opts Options) (*control.Client, error) {
	if opts.ControlSocket != "" {
		token := ""
		if ep, err := control.ReadEndpoint(opts.RunDir); err == nil {
			token = ep.Token
		}
		return control.DialAddr(ctx, opts.ControlSocket, token)
	}
	return control.Dial(ctx, opts.RunDir)
}

// isTerminal reports whether f is a character device (a TTY or a console).
// It avoids a dependency on x/term for the one question the TUI asks.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
