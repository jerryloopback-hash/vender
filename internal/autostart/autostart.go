// Package autostart registers and inspects vender's start-at-login entry.
//
// Per-platform mechanism:
//
//	Linux   a systemd user unit (vender.service, WantedBy=default.target) when
//	        systemd is available, otherwise an XDG autostart desktop entry in
//	        $XDG_CONFIG_HOME/autostart/vender.desktop.
//	Windows a Task Scheduler logon task that runs at logon without a console
//	        window when it can be created, otherwise the per-user Registry Run
//	        key (HKCU\Software\Microsoft\Windows\CurrentVersion\Run). Neither
//	        needs administrator rights.
//
// The entry only ever has to run `vender daemon`. That program is headless (no
// console, no TTY), takes an advisory lock on its run directory so two copies
// cannot fight, and exits cleanly on SIGTERM — which is exactly what a systemd
// unit sends. Its control channel is loopback TCP, so a client started in a
// later login session can reach the daemon autostart started.
//
// Enable and Disable are idempotent: enabling twice leaves one entry, disabling
// what is not there is not an error. Status never fails merely because nothing
// is registered — it reports Enabled=false along with the mechanism and target
// that `on` would use, so `vender autostart status` can always explain itself.
package autostart

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
	// MechanismWindowsTask is a Task Scheduler logon task (Windows).
	MechanismWindowsTask Mechanism = "windows-task"
	// MechanismWindowsRunKey is the per-user Registry Run key (Windows).
	MechanismWindowsRunKey Mechanism = "windows-run-key"
)

// State describes the current autostart configuration.
type State struct {
	// Enabled reports whether vender is registered to start automatically.
	Enabled bool `json:"enabled"`
	// Mechanism is how it is registered (or, when Enabled is false, how it
	// would be).
	Mechanism Mechanism `json:"mechanism"`
	// Target is the concrete artifact: the unit file path, the desktop entry
	// path, the task name (`\vender\daemon`), or the registry value name.
	Target string `json:"target,omitempty"`
	// Detail is extra human-readable information for `autostart status`: why
	// the mechanism was chosen, whether lingering is on, whether the launch
	// will be windowless, and anything that could not be arranged.
	Detail string `json:"detail,omitempty"`
}
