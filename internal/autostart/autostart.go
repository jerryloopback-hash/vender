// Package autostart will register and inspect vender's start-at-login entry
// (M4).
//
// Per-platform mechanism:
//
//	Linux   a systemd user unit (vender.service, WantedBy=default.target) when
//	        systemd is available, otherwise an XDG autostart desktop entry in
//	        $XDG_CONFIG_HOME/autostart/vender.desktop.
//	Windows the per-user Registry Run key
//	        (HKCU\Software\Microsoft\Windows\CurrentVersion\Run) or, when
//	        elevation is wanted, a Task Scheduler entry.
//
// M0 ships only the types and the entry points, all returning
// ErrNotImplemented, so the CLI surface (`vender autostart on|off|status`) is
// already stable.
package autostart

import "errors"

// ErrNotImplemented is returned by every entry point until M4 lands.
var ErrNotImplemented = errors.New("autostart: not implemented in this milestone (M4)")

// Mechanism identifies how autostart is (or would be) registered.
type Mechanism string

const (
	// MechanismUnknown means autostart is not configured, or its mechanism
	// could not be determined.
	MechanismUnknown Mechanism = "unknown"
	// MechanismSystemdUser is a systemd --user unit (Linux).
	MechanismSystemdUser Mechanism = "systemd-user"
	// MechanismXDGAutostart is an XDG autostart desktop entry (Linux).
	MechanismXDGAutostart Mechanism = "xdg-autostart"
	// MechanismWindowsRunKey is the per-user Registry Run key (Windows).
	MechanismWindowsRunKey Mechanism = "windows-run-key"
	// MechanismWindowsTask is a Task Scheduler entry (Windows).
	MechanismWindowsTask Mechanism = "windows-task"
)

// State describes the current autostart configuration.
type State struct {
	// Enabled reports whether vender is registered to start automatically.
	Enabled bool
	// Mechanism is how it is registered.
	Mechanism Mechanism
	// Target is the concrete artifact, e.g. the unit file path, the desktop
	// entry path, or the registry value name.
	Target string
	// Detail is extra human-readable information for `autostart status`.
	Detail string
}

// Enable registers vender to start the daemon at login. It is idempotent.
func Enable() error { return ErrNotImplemented }

// Disable removes the autostart registration. It is idempotent.
func Disable() error { return ErrNotImplemented }

// Status reports the current autostart registration.
func Status() (State, error) { return State{Mechanism: MechanismUnknown}, ErrNotImplemented }
