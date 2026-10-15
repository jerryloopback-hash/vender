// Package tui will provide vender's optional Bubble Tea terminal UI (M3).
//
// The TUI is a *client*: it never talks to sing-box directly. It reads and
// writes the same configuration files as the CLI (through internal/config) and
// sends control requests (start/stop/switch server/mode) to the running daemon
// over a local control socket. That keeps the daemon authoritative and lets the
// TUI attach, detach and restart freely.
package tui

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by Run until M3 lands.
var ErrNotImplemented = errors.New("tui: not implemented in this milestone (M3)")

// Options configures one TUI session.
type Options struct {
	// ConfigDir is the vender configuration directory holding config.yaml and
	// servers.yaml.
	ConfigDir string
	// ControlSocket is the daemon's local control endpoint that the TUI
	// attaches to. Empty means "derive the platform default" (a unix socket on
	// Linux, a named pipe on Windows).
	ControlSocket string
	// RefreshInterval is how often the TUI polls daemon state, in the daemon's
	// own unit (M3 decides; zero means "use the default").
	RefreshInterval int
}

// Run starts the interactive TUI and blocks until the user quits or ctx is
// cancelled. It is a stub in M0.
func Run(ctx context.Context, opts Options) error {
	_, _ = ctx, opts
	return ErrNotImplemented
}
