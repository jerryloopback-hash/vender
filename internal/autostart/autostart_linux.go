//go:build linux

package autostart

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	user      string // login name, for loginctl
	exe       string // absolute path to the vender binary
	lookPath  func(string) (string, error)
	run       cmdRunner
}

func defaultLinuxBackend() linuxBackend {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	} else if abs, aerr := filepath.Abs(exe); aerr == nil {
		exe = abs
	}
	return linuxBackend{
		home:      os.Getenv("HOME"),
		xdgConfig: os.Getenv("XDG_CONFIG_HOME"),
		user:      loginName(),
		exe:       exe,
		lookPath:  exec.LookPath,
		run:       runCommand,
	}
}

// Enable registers vender to start the daemon at login. It is idempotent.
func Enable() error {
	_, err := defaultLinuxBackend().enable()
	return err
}

// Disable removes the autostart registration. It is idempotent.
func Disable() error { return defaultLinuxBackend().disable() }

// Status reports the current autostart registration.
func Status() (State, error) { return defaultLinuxBackend().status() }

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

func (b linuxBackend) enable() (State, error) {
	if b.exe == "" {
		return State{}, errors.New("autostart: cannot determine the vender executable path (os.Executable failed)")
	}
	if b.systemdAvailable() {
		return b.enableSystemd()
	}
	return b.enableXDG()
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

// disable removes both artifacts. Removing the one that is not there is not an
// error, which is what makes `off` idempotent.
func (b linuxBackend) disable() error {
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

func (b linuxBackend) status() (State, error) {
	unitPath, uerr := b.unitPath()
	desktopPath, derr := b.desktopPath()

	if uerr == nil && fileExists(unitPath) {
		return b.systemdState(unitPath, true), nil
	}
	if derr == nil && fileExists(desktopPath) {
		return State{
			Enabled:   true,
			Mechanism: MechanismXDGAutostart,
			Target:    desktopPath,
			Detail:    "XDG autostart desktop entry installed",
		}, nil
	}

	// Nothing is installed: report what `on` would do here, so the command is
	// still informative.
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
