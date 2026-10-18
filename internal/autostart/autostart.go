// Package autostart registers and inspects vender's start-at-login entry.
//
// Per-platform mechanism:
//
//	Linux   a systemd user unit (vender.service, WantedBy=default.target) when
//	        systemd is available, otherwise an XDG autostart desktop entry in
//	        $XDG_CONFIG_HOME/autostart/vender.desktop. With TUN mode enabled a
//	        systemd *user* unit is not enough — capabilities are granted by
//	        systemd to system services — so a *system* unit is installed instead
//	        (User=<you>, AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW), which
//	        needs root once.
//	Windows a Task Scheduler logon task that runs at logon without a console
//	        window when it can be created, otherwise the per-user Registry Run
//	        key. With TUN mode enabled the task is registered as
//	        HighestAvailable, which needs one elevated registration and cannot
//	        be replaced by the Run key.
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
//
// # The TUN trade-off, stated plainly
//
// A systemd user unit runs with the user's own privileges and cannot be given
// capabilities: the user manager itself is unprivileged, so it has nothing to
// hand out. Two mechanisms therefore exist for TUN mode:
//
//   - a system unit (installed by `sudo vender autostart on`) that runs the
//     daemon as the invoking user with exactly CAP_NET_ADMIN and CAP_NET_RAW
//     added — the privileged path, and the one that keeps working across
//     sing-box upgrades; and
//   - the user unit plus `setcap cap_net_admin,cap_net_raw+ep sing-box`, which
//     needs no service in /etc but is lost whenever the engine binary is
//     replaced (a sing-box upgrade, a reinstall, a rebuilt release archive).
//
// autostart prefers the first and falls back to the second, reporting which one
// is in effect and whether it can actually create a TUN. It never claims a
// working setup it cannot deliver: if TUN is on and the registered mechanism
// cannot provide the capability, `status` says so and Enable returns an error
// naming the fix.
package autostart

// Mechanism identifies how autostart is (or would be) registered.
type Mechanism string

const (
	// MechanismUnknown means autostart is not configured, or its mechanism
	// could not be determined.
	MechanismUnknown Mechanism = "unknown"
	// MechanismSystemdUser is a systemd --user unit (Linux).
	MechanismSystemdUser Mechanism = "systemd-user"
	// MechanismSystemdSystem is a systemd *system* unit (Linux), used when TUN
	// mode is enabled: only a system service can be given CAP_NET_ADMIN.
	MechanismSystemdSystem Mechanism = "systemd-system"
	// MechanismXDGAutostart is an XDG autostart desktop entry (Linux).
	MechanismXDGAutostart Mechanism = "xdg-autostart"
	// MechanismWindowsTask is a Task Scheduler logon task (Windows).
	MechanismWindowsTask Mechanism = "windows-task"
	// MechanismWindowsRunKey is the per-user Registry Run key (Windows).
	MechanismWindowsRunKey Mechanism = "windows-run-key"
)

// Options tells autostart what the daemon will need when it starts.
//
// It exists because one thing changes the right mechanism: TUN mode needs a
// kernel privilege (CAP_NET_ADMIN on Linux, Administrator on Windows), and the
// default per-user mechanisms cannot provide it. The caller reads config.yaml
// and says so; autostart does not depend on internal/config.
type Options struct {
	// Explicit directories are preserved in Windows task/Run-key arguments.
	ConfigDir string
	RunDir    string
	// TUN reports whether the TUN inbound is enabled in config.yaml.
	TUN bool
	// CapabilityHint is the caller's one-line verdict on whether this
	// environment can grant the engine CAP_NET_ADMIN (see
	// core.CheckTUNPrivilege). It is quoted by Status when TUN is on but the
	// registered mechanism cannot provide the privilege, so a user is told
	// exactly what is missing instead of discovering it at the next login.
	// Optional.
	CapabilityHint string
}

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
