//go:build linux

package autostart

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// userLookup is a seam for homeOf, so a test does not need a real account.
var userLookup = user.Lookup

// The systemd unit is named after the program, so `systemctl --user status
// vender` does what a user expects.
const systemdUnitName = "vender.service"

// cmdRunner runs an external command and returns its combined output. It is a
// seam: the tests drive the whole implementation with a recorder instead of a
// real systemctl, which is what keeps `go test` from touching the host's
// systemd.
type cmdRunner func(name string, args ...string) (string, error)

// linuxBackend is every ambient input the Linux implementation reads: the two
// config locations, the path of the vender binary the unit should exec, the
// login name for logind, and the ability to look up and run external commands.
// It is a value with injectable fields, so a test can point HOME and
// XDG_CONFIG_HOME at a temporary directory and put a stub `systemctl` on PATH
// without the package needing a global environment hook.
type linuxBackend struct {
	home      string // $HOME
	xdgConfig string // $XDG_CONFIG_HOME (may be empty)
	user      string // login name, for the system unit's User= and for loginctl
	exe       string // absolute path to the vender binary
	// systemUnitDir is where the *system* unit lives. It is a field so the
	// tests can install a system unit without root.
	systemUnitDir string
	// euid is os.Geteuid(); 0 means "root, so the system unit can be written".
	// A field so a test can act as root.
	euid     int
	lookPath func(string) (string, error)
	run      cmdRunner
}

// systemdSystemUnitDir is the system manager's unit directory.
const systemdSystemUnitDir = "/etc/systemd/system"

func defaultLinuxBackend() linuxBackend {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	} else if abs, aerr := filepath.Abs(exe); aerr == nil {
		exe = abs
	}
	user := loginName()
	home := os.Getenv("HOME")

	// Under sudo the interesting user is the one who invoked it: the config
	// directory, the run directory and the User= of a system unit all belong to
	// them, not to root. `sudo vender autostart on` therefore has to look at
	// the *invoking* user's configuration.
	if os.Geteuid() == 0 {
		if su := strings.TrimSpace(os.Getenv("SUDO_USER")); su != "" && su != "root" {
			user = su
			if h := homeOf(su); h != "" {
				home = h
			}
		}
	}
	return linuxBackend{
		home:          home,
		xdgConfig:     os.Getenv("XDG_CONFIG_HOME"),
		user:          user,
		exe:           exe,
		systemUnitDir: systemdSystemUnitDir,
		euid:          os.Geteuid(),
		lookPath:      exec.LookPath,
		run:           runCommand,
	}
}

// homeOf resolves a login name to its home directory. A failure is not fatal
// (the unit then simply does not pin HOME), so the error is dropped.
func homeOf(user string) string {
	u, err := userLookup(user)
	if err != nil {
		return ""
	}
	return u.HomeDir
}

// Enable registers vender to start the daemon at login. It is idempotent.
func Enable() error { return EnableWithOptions(Options{}) }

// EnableWithOptions registers vender with the mechanism the Options call for:
// the per-user one by default, a system unit when TUN mode needs the
// capability.
func EnableWithOptions(o Options) error {
	_, err := defaultLinuxBackend().enableWith(o)
	return err
}

// Disable removes the autostart registration. It is idempotent.
func Disable() error { return defaultLinuxBackend().disable() }

// Status reports the current autostart registration.
func Status() (State, error) { return StatusWithOptions(Options{}) }

// StatusWithOptions reports the registration *and* whether it can deliver what
// the current configuration needs, which is the only honest answer once TUN
// mode is in play.
func StatusWithOptions(o Options) (State, error) {
	return defaultLinuxBackend().statusWith(o)
}

func runCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// loginName is what loginctl wants; $USER is set by every login session, with
// $LOGNAME as the conventional fallback.
func loginName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("LOGNAME")
}

// --- locations --------------------------------------------------------------

// configHome resolves $XDG_CONFIG_HOME, defaulting to $HOME/.config as the XDG
// base directory spec requires.
func (b linuxBackend) configHome() (string, error) {
	if b.xdgConfig != "" {
		return b.xdgConfig, nil
	}
	if b.home == "" {
		return "", errors.New("autostart: neither $XDG_CONFIG_HOME nor $HOME is set")
	}
	return filepath.Join(b.home, ".config"), nil
}

func (b linuxBackend) unitPath() (string, error) {
	home, err := b.configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "systemd", "user", systemdUnitName), nil
}

// systemUnitPath is the *system* manager's unit file. It has the same name as
// the user unit because the two live in different managers, which keeps
// `systemctl status vender` (system) and `systemctl --user status vender`
// meaningful and does not need two names in the documentation.
func (b linuxBackend) systemUnitPath() string {
	dir := b.systemUnitDir
	if dir == "" {
		dir = systemdSystemUnitDir
	}
	return filepath.Join(dir, systemdUnitName)
}

func (b linuxBackend) desktopPath() (string, error) {
	home, err := b.configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "autostart", "vender.desktop"), nil
}

// --- systemd detection ------------------------------------------------------

// systemdAvailable reports whether a systemd *user* manager answers here.
//
// The binary must exist and `systemctl --user` must actually answer: a
// container can have the client installed with no session bus behind it. A
// manager that is up but degraded still accepts units, so "degraded" counts as
// available while a bus-connection failure does not.
func (b linuxBackend) systemdAvailable() bool {
	if _, err := b.lookPath("systemctl"); err != nil {
		return false
	}
	out, err := b.run("systemctl", "--user", "is-system-running")
	if err == nil {
		return true
	}
	switch strings.TrimSpace(out) {
	case "degraded", "starting", "maintenance":
		return true
	}
	return false
}

// --- enable -----------------------------------------------------------------

func (b linuxBackend) enable() (State, error) { return b.enableWith(Options{}) }

// enableWith picks the mechanism. TUN mode changes the answer: a systemd *user*
// unit cannot be given CAP_NET_ADMIN, so the registration has to be a system
// unit; if that cannot be installed, the user registration is still made (the
// daemon does start at login) and the returned error says what TUN still needs.
func (b linuxBackend) enableWith(o Options) (State, error) {
	if b.exe == "" {
		return State{}, errors.New("autostart: cannot determine the vender executable path (os.Executable failed)")
	}
	if o.TUN {
		return b.enableSystemUnit(o)
	}
	return b.enableUserUnit()
}

// enableUserUnit is the default, unprivileged mechanism.
func (b linuxBackend) enableUserUnit() (State, error) {
	if b.systemdAvailable() {
		return b.enableSystemd()
	}
	return b.enableXDG()
}

// enableSystemUnit installs the TUN-capable registration: a system service that
// runs the daemon as the invoking user with exactly the capabilities a TUN
// needs.
//
// It falls back to the per-user registration when the system unit cannot be
// installed (the usual case: not root), and returns an error describing what is
// missing rather than reporting a success that will not survive the next login.
func (b linuxBackend) enableSystemUnit(o Options) (State, error) {
	st, err := b.installSystemUnit()
	if err == nil {
		// A leftover user unit would fight the system one for the run
		// directory's lock, so the now-superseded per-user entries go.
		b.removeUserArtifacts()
		return st, nil
	}

	fallback, ferr := b.enableUserUnit()
	if ferr != nil {
		return State{}, fmt.Errorf("autostart: TUN mode needs a system unit (%v), and the per-user fallback also failed: %w", err, ferr)
	}
	fallback.Detail += "; TUN is enabled but this registration cannot create a TUN device: " + b.capabilityNote(o)
	return fallback, fmt.Errorf("autostart: TUN mode needs CAP_NET_ADMIN, which a systemd user unit cannot be given (%v); "+
		"run `sudo vender autostart on` to install the system unit. Meanwhile: %s", err, b.capabilityNote(o))
}

// installSystemUnit writes /etc/systemd/system/vender.service and enables it.
func (b linuxBackend) installSystemUnit() (State, error) {
	path := b.systemUnitPath()
	if b.euid != 0 {
		return State{}, fmt.Errorf("uid %d cannot write %s: run `sudo vender autostart on`", b.euid, path)
	}
	if err := writeFileAtomic(path, systemdSystemUnit(b.exe, b.user, b.home)); err != nil {
		return State{}, err
	}
	// From here on a failure must not leave a unit file behind: the caller
	// falls back to the per-user registration, and two registrations (one of
	// them a disabled system unit) would make `status` and the next boot
	// disagree about what is installed.
	if out, err := b.run("systemctl", "daemon-reload"); err != nil {
		_ = removeFile(path)
		return State{}, fmt.Errorf("autostart: systemctl daemon-reload: %w%s", err, outputSuffix(out))
	}
	if out, err := b.run("systemctl", "enable", "--now", systemdUnitName); err != nil {
		_ = removeFile(path)
		_, _ = b.run("systemctl", "daemon-reload")
		return State{}, fmt.Errorf("autostart: systemctl enable --now %s: %w%s", systemdUnitName, err, outputSuffix(out))
	}
	return State{
		Enabled:   true,
		Mechanism: MechanismSystemdSystem,
		Target:    path,
		Detail: "system unit installed and enabled (User=" + b.user +
			", AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW) because TUN mode is on; " +
			"it starts the daemon at boot and is the only mechanism a systemd user manager cannot provide a TUN for",
	}, nil
}

// capabilityNote describes what TUN mode still needs, quoting the caller's
// verdict when it has one.
func (b linuxBackend) capabilityNote(o Options) string {
	if o.CapabilityHint != "" {
		return o.CapabilityHint
	}
	return "the engine needs CAP_NET_ADMIN (run `vender tun status` for the exact commands)"
}

func (b linuxBackend) enableSystemd() (State, error) {
	unitPath, err := b.unitPath()
	if err != nil {
		return State{}, err
	}
	if err := writeFileAtomic(unitPath, systemdUnit(b.exe)); err != nil {
		return State{}, err
	}
	// The unit file is the source of truth; reload makes systemd see it, and
	// enable --now starts it immediately (and again at every login).
	if out, err := b.run("systemctl", "--user", "daemon-reload"); err != nil {
		return State{}, fmt.Errorf("autostart: systemctl --user daemon-reload: %w%s", err, outputSuffix(out))
	}
	if out, err := b.run("systemctl", "--user", "enable", "--now", systemdUnitName); err != nil {
		return State{}, fmt.Errorf("autostart: systemctl --user enable --now %s: %w%s", systemdUnitName, err, outputSuffix(out))
	}

	// Start-at-boot-without-login needs lingering. It is best-effort: without
	// polkit a non-interactive call is refused, and that must not undo a
	// registration that is otherwise correct.
	note := b.ensureLinger()

	st := State{
		Enabled:   true,
		Mechanism: MechanismSystemdUser,
		Target:    unitPath,
		Detail:    "systemd user unit installed and enabled; the daemon starts at login",
	}
	if note != "" {
		st.Detail += "; " + note
	}
	return st, nil
}

func (b linuxBackend) enableXDG() (State, error) {
	path, err := b.desktopPath()
	if err != nil {
		return State{}, err
	}
	if err := writeFileAtomic(path, xdgDesktopEntry(b.exe)); err != nil {
		return State{}, err
	}
	return State{
		Enabled:   true,
		Mechanism: MechanismXDGAutostart,
		Target:    path,
		Detail: "XDG autostart desktop entry installed (systemd user manager not available); " +
			"it starts the daemon when a graphical session begins and needs a desktop that reads $XDG_CONFIG_HOME/autostart",
	}, nil
}

// --- disable ----------------------------------------------------------------

// disable removes every artifact: the system unit (which needs root), the user
// unit and the desktop entry. Removing one that is not there is not an error,
// which is what makes `off` idempotent.
func (b linuxBackend) disable() error {
	var firstErr error
	if err := b.removeSystemUnit(); err != nil {
		firstErr = err
	}
	if err := b.removeUserArtifacts(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// removeSystemUnit removes the TUN-capable registration. It needs root, and
// says so rather than silently leaving a root-owned unit behind that would
// start the daemon again at the next boot.
func (b linuxBackend) removeSystemUnit() error {
	path := b.systemUnitPath()
	if !fileExists(path) {
		return nil
	}
	if b.euid != 0 {
		return fmt.Errorf("the system unit %s needs root to remove: run `sudo vender autostart off`", path)
	}
	_, _ = b.run("systemctl", "disable", "--now", systemdUnitName)
	if err := removeFile(path); err != nil {
		return err
	}
	_, _ = b.run("systemctl", "daemon-reload")
	return nil
}

// removeUserArtifacts removes the user unit and the XDG entry, best-effort.
func (b linuxBackend) removeUserArtifacts() error {
	if b.systemdAvailable() {
		if unitPath, err := b.unitPath(); err == nil {
			if _, statErr := os.Stat(unitPath); statErr == nil {
				// Stop it now as well: leaving a running daemon behind after
				// `autostart off` would be surprising. Best-effort, because a
				// missing polkit/dbus must not strand the unit file.
				_, _ = b.run("systemctl", "--user", "disable", "--now", systemdUnitName)
			}
			if rmErr := removeFile(unitPath); rmErr != nil {
				return rmErr
			}
			_, _ = b.run("systemctl", "--user", "daemon-reload")
		}
	}
	if path, err := b.desktopPath(); err == nil {
		if rmErr := removeFile(path); rmErr != nil {
			return rmErr
		}
	}
	return nil
}

// --- status -----------------------------------------------------------------

func (b linuxBackend) status() (State, error) { return b.statusWith(Options{}) }

func (b linuxBackend) statusWith(o Options) (State, error) {
	unitPath, uerr := b.unitPath()
	desktopPath, derr := b.desktopPath()

	// The system unit wins when both exist: it is the one serving a running
	// daemon whose lock would make the user unit fail anyway.
	if path := b.systemUnitPath(); fileExists(path) {
		return b.systemUnitState(path, o), nil
	}
	if uerr == nil && fileExists(unitPath) {
		st := b.systemdState(unitPath, true)
		if o.TUN {
			st.Detail += "; TUN is enabled, but a systemd *user* unit cannot create a TUN device — " +
				b.capabilityNote(o) + "; run `sudo vender autostart on` to install the system unit instead"
		}
		return st, nil
	}
	if derr == nil && fileExists(desktopPath) {
		st := State{
			Enabled:   true,
			Mechanism: MechanismXDGAutostart,
			Target:    desktopPath,
			Detail:    "XDG autostart desktop entry installed",
		}
		if o.TUN {
			st.Detail += "; TUN is enabled, but an XDG desktop entry cannot create a TUN device — " +
				b.capabilityNote(o) + "; run `sudo vender autostart on` to install a system unit instead"
		}
		return st, nil
	}

	// Nothing is installed: report what `on` would do here, so the command is
	// still informative.
	if o.TUN {
		return State{
			Enabled:   false,
			Mechanism: MechanismSystemdSystem,
			Target:    b.systemUnitPath(),
			Detail: "not registered; with TUN enabled, `vender autostart on` installs a system unit " +
				"(needs root once) so the daemon can have CAP_NET_ADMIN. Without root it falls back to the " +
				"per-user registration, which also needs " + b.capabilityNote(o),
		}, nil
	}
	if b.systemdAvailable() && uerr == nil {
		return b.systemdState(unitPath, false), nil
	}
	if derr == nil {
		return State{
			Enabled:   false,
			Mechanism: MechanismXDGAutostart,
			Target:    desktopPath,
			Detail:    "not registered; `vender autostart on` would write an XDG autostart desktop entry (no systemd user manager answered)",
		}, nil
	}
	return State{
		Enabled:   false,
		Mechanism: MechanismUnknown,
		Detail:    "not registered, and neither $XDG_CONFIG_HOME nor $HOME is set, so the entry cannot be placed",
	}, nil
}

// systemUnitState reports a registered system unit, including what systemd
// thinks of it, so `autostart status` can be checked against reality.
func (b linuxBackend) systemUnitState(path string, o Options) State {
	st := State{Enabled: true, Mechanism: MechanismSystemdSystem, Target: path}
	parts := []string{"system unit installed (User=" + b.user + ", AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW)"}
	if out, err := b.run("systemctl", "is-enabled", systemdUnitName); err == nil && out != "" {
		parts = append(parts, "systemctl is-enabled: "+out)
	}
	if out, err := b.run("systemctl", "is-active", systemdUnitName); err == nil && out != "" {
		parts = append(parts, "systemctl is-active: "+out)
	}
	if o.TUN {
		parts = append(parts, "it can create a TUN device (the capability is part of the unit)")
	} else {
		parts = append(parts, "TUN is off, so the granted capabilities are unused; the per-user unit would do")
	}
	st.Detail = strings.Join(parts, "; ")
	return st
}

func (b linuxBackend) systemdState(unitPath string, enabled bool) State {
	st := State{Enabled: enabled, Mechanism: MechanismSystemdUser, Target: unitPath}
	var parts []string
	if enabled {
		parts = append(parts, "systemd user unit installed (WantedBy=default.target, ExecStart="+systemdExec(b.exe)+" daemon)")
	} else {
		parts = append(parts, "not registered; `vender autostart on` would install a systemd user unit")
	}
	if enabled && b.systemdAvailable() {
		if out, err := b.run("systemctl", "--user", "is-enabled", systemdUnitName); err == nil && out != "" {
			parts = append(parts, "systemctl --user is-enabled: "+out)
		}
	}
	parts = append(parts, "lingering: "+b.lingerStatus()+" (needed to start at boot without a graphical login)")
	st.Detail = strings.Join(parts, "; ")
	return st
}

// lingerStatus reports whether logind keeps this user's manager alive between
// logins, which is what "start at boot, not just at login" requires.
func (b linuxBackend) lingerStatus() string {
	if b.user == "" {
		return "unknown (no $USER)"
	}
	if _, err := b.lookPath("loginctl"); err != nil {
		return "unknown (loginctl not found)"
	}
	out, err := b.run("loginctl", "show-user", b.user, "-p", "Linger")
	if err != nil {
		return "unknown"
	}
	if strings.Contains(out, "Linger=yes") {
		return "on"
	}
	return "off"
}

// ensureLinger asks logind for lingering and returns a human note when it could
// not be arranged, so the caller can put it in Status.Detail.
func (b linuxBackend) ensureLinger() string {
	if _, err := b.lookPath("loginctl"); err != nil {
		return "lingering not requested (loginctl not found)"
	}
	args := []string{"enable-linger"}
	if b.user != "" {
		args = append(args, b.user)
	}
	if out, err := b.run("loginctl", args...); err != nil {
		msg := firstLine(out)
		if msg == "" {
			msg = err.Error()
		}
		return "lingering not enabled (" + msg + "); run `loginctl enable-linger " +
			nonEmpty(b.user, "$USER") + "` to start at boot without logging in"
	}
	return ""
}

// --- file contents ----------------------------------------------------------

// systemdUnit is the unit autostart installs. It is deliberately minimal: the
// daemon is headless, needs no working directory or environment beyond the
// user's, and is restarted on failure rather than on a timer.
func systemdUnit(exe string) string {
	return "[Unit]\n" +
		"Description=vender proxy client\n" +
		"Documentation=https://github.com/ll-lite/vender\n" +
		"After=network-online.target\n" +
		"Wants=network-online.target\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + systemdExec(exe) + " daemon\n" +
		"Restart=on-failure\n" +
		"RestartSec=3\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}

// systemdExec renders a path as a single systemd command word. systemd splits
// ExecStart on whitespace and understands double quotes with C-style escapes,
// so a path containing a space has to be quoted.
func systemdExec(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + esc + `"`
}

// systemdSystemUnit is the TUN-capable unit. It runs the daemon as the invoking
// user — not as root — and gives it exactly the two capabilities a TUN needs,
// which is why it is preferred over running the whole daemon privileged.
//
//   - User= plus AmbientCapabilities= is the combination that works: systemd
//     (as root) sets the ambient set, and ambient capabilities survive the
//     setuid to the target user. CapabilityBoundingSet keeps everything else
//     out of reach, so a compromise of the daemon does not become a compromise
//     of the machine.
//   - Environment=HOME= pins the user's home directory, so the daemon resolves
//     the same config and run directories the user's own `vender` commands use
//     even when systemd does not set up a login environment.
//   - WantedBy=multi-user.target starts it at boot, before any login, which is
//     what a system-wide TUN is usually wanted for.
func systemdSystemUnit(exe, user, home string) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=vender proxy client (TUN mode)\n")
	b.WriteString("Documentation=https://github.com/ll-lite/vender\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	if user != "" {
		b.WriteString("User=" + user + "\n")
	}
	if home != "" {
		b.WriteString("Environment=HOME=" + systemdEnvValue(home) + "\n")
	}
	b.WriteString("AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW\n")
	b.WriteString("CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW\n")
	b.WriteString("ExecStart=" + systemdExec(exe) + " daemon\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=3\n")
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// systemdEnvValue quotes a value for systemd's Environment= assignment, which
// is whitespace-split and understands C-style escapes inside double quotes.
func systemdEnvValue(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + esc + `"`
}

// xdgDesktopEntry is the fallback a desktop session reads when there is no
// systemd user manager installed.
func xdgDesktopEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=vender\n" +
		"Comment=vender proxy client (start at login)\n" +
		"Exec=" + desktopExec(exe) + " daemon\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// desktopExec renders the executable for a Desktop Entry Exec= line. The spec
// reserves `"`, backtick, `$` and `\` inside a double-quoted argument.
func desktopExec(s string) string {
	if !strings.ContainsAny(s, " \t\"'`$\\") {
		return s
	}
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(s)
	return `"` + esc + `"`
}

// --- small helpers ----------------------------------------------------------

// writeFileAtomic writes content through a temporary file in the same directory
// and renames it into place, so a crash or a concurrent reader never sees a
// half-written unit. The directory is created when missing.
func writeFileAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("autostart: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".vender-autostart.*")
	if err != nil {
		return fmt.Errorf("autostart: create a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has happened

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("autostart: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("autostart: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("autostart: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("autostart: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("autostart: install %s: %w", path, err)
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("autostart: remove %s: %w", path, err)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func outputSuffix(out string) string {
	if msg := firstLine(out); msg != "" {
		return ": " + msg
	}
	return ""
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
