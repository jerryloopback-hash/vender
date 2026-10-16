//go:build linux

package autostart

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRun is a scripted command runner: it records every command and answers
// from a table, so the tests exercise the real enable/disable/status flow
// without touching systemd, logind or the network.
type fakeRun struct {
	calls   []string
	answers map[string]string // "name arg arg" -> output
	fail    map[string]bool   // "name arg arg" -> non-zero exit
}

func newFakeRun() *fakeRun {
	return &fakeRun{answers: map[string]string{}, fail: map[string]bool{}}
}

func (f *fakeRun) run(name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	if f.fail[key] {
		return f.answers[key], errors.New("exit status 1")
	}
	return f.answers[key], nil
}

func (f *fakeRun) ran(want string) bool {
	for _, c := range f.calls {
		if c == want {
			return true
		}
	}
	return false
}

// systemdBackend is an isolated HOME with a "systemd is here" stub.
func systemdBackend(t *testing.T, r *fakeRun) (linuxBackend, string) {
	t.Helper()
	home := t.TempDir()
	r.answers["systemctl --user is-system-running"] = "running"
	r.answers["systemctl --user is-enabled vender.service"] = "enabled"
	r.answers["loginctl show-user tester -p Linger"] = "Linger=yes"
	look := func(name string) (string, error) {
		switch name {
		case "systemctl", "loginctl":
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	return linuxBackend{
		home:     home,
		exe:      "/usr/local/bin/vender",
		user:     "tester",
		lookPath: look,
		run:      r.run,
	}, home
}

func TestEnableWritesASystemdUserUnit(t *testing.T) {
	r := newFakeRun()
	b, home := systemdBackend(t, r)

	st, err := b.enable()
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !st.Enabled || st.Mechanism != MechanismSystemdUser {
		t.Errorf("state = %+v", st)
	}

	path := filepath.Join(home, ".config", "systemd", "user", "vender.service")
	if st.Target != path {
		t.Errorf("target = %q, want %q", st.Target, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"ExecStart=/usr/local/bin/vender daemon",
		"Restart=on-failure",
		"RestartSec=3",
		"WantedBy=default.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unit file is missing %q:\n%s", want, got)
		}
	}

	for _, want := range []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now vender.service",
		"loginctl enable-linger tester",
	} {
		if !r.ran(want) {
			t.Errorf("did not run %q (ran: %v)", want, r.calls)
		}
	}
}

func TestStatusReportsTheInstalledUnit(t *testing.T) {
	r := newFakeRun()
	b, _ := systemdBackend(t, r)

	// Nothing yet: disabled, but it names the mechanism and the target it
	// would use, and explains itself.
	st, err := b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Enabled || st.Mechanism != MechanismSystemdUser || st.Target == "" {
		t.Errorf("disabled state = %+v", st)
	}
	if !strings.Contains(st.Detail, "not registered") {
		t.Errorf("detail = %q", st.Detail)
	}

	if _, err := b.enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	st, err = b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("status after enable = %+v", st)
	}
	for _, want := range []string{"is-enabled: enabled", "lingering: on"} {
		if !strings.Contains(st.Detail, want) {
			t.Errorf("detail = %q, want %q", st.Detail, want)
		}
	}
}

func TestDisableRemovesTheUnitAndStopsIt(t *testing.T) {
	r := newFakeRun()
	b, home := systemdBackend(t, r)

	if _, err := b.enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := b.disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	path := filepath.Join(home, ".config", "systemd", "user", "vender.service")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unit file still present after disable (stat = %v)", err)
	}
	if !r.ran("systemctl --user disable --now vender.service") {
		t.Errorf("disable did not stop the unit (ran: %v)", r.calls)
	}
	st, err := b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Enabled {
		t.Errorf("status after disable = %+v", st)
	}
}

func TestEnableAndDisableAreIdempotent(t *testing.T) {
	r := newFakeRun()
	b, home := systemdBackend(t, r)

	for i := 0; i < 2; i++ {
		if _, err := b.enable(); err != nil {
			t.Fatalf("enable #%d: %v", i+1, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".config", "systemd", "user"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var units []string
	for _, e := range entries {
		units = append(units, e.Name())
	}
	if len(units) != 1 || units[0] != "vender.service" {
		t.Errorf("unit dir = %v, want exactly vender.service", units)
	}

	if err := b.disable(); err != nil {
		t.Fatalf("disable #1: %v", err)
	}
	if err := b.disable(); err != nil {
		t.Fatalf("disable #2: %v", err)
	}
}

func TestXDGFallbackWhenSystemdIsUnavailable(t *testing.T) {
	home := t.TempDir()
	r := newFakeRun()
	r.answers["systemctl --user is-system-running"] = "Failed to connect to bus: No such file or directory"
	r.fail["systemctl --user is-system-running"] = true
	look := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	b := linuxBackend{home: home, exe: "/opt/vender", user: "tester", lookPath: look, run: r.run}

	st, err := b.enable()
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if st.Mechanism != MechanismXDGAutostart {
		t.Errorf("mechanism = %q, want xdg-autostart", st.Mechanism)
	}
	desktop := filepath.Join(home, ".config", "autostart", "vender.desktop")
	body, err := os.ReadFile(desktop)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range []string{"[Desktop Entry]", "Type=Application", "Exec=/opt/vender daemon", "Terminal=false"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("desktop entry is missing %q:\n%s", want, body)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "vender.service")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a systemd unit was written even though the manager is unavailable")
	}

	// status and disable follow the same path.
	st, err = b.status()
	if err != nil || !st.Enabled || st.Mechanism != MechanismXDGAutostart {
		t.Errorf("status = %+v, %v", st, err)
	}
	if err := b.disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(desktop); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("desktop entry still present after disable")
	}
}

func TestDegradedUserManagerStillAcceptsUnits(t *testing.T) {
	r := newFakeRun()
	b, _ := systemdBackend(t, r)
	r.answers["systemctl --user is-system-running"] = "degraded"
	r.fail["systemctl --user is-system-running"] = true
	if !b.systemdAvailable() {
		t.Error("a degraded user manager should count as available")
	}

	r.answers["systemctl --user is-system-running"] = "Failed to connect to bus"
	if b.systemdAvailable() {
		t.Error("a bus failure should not count as available")
	}
}

func TestDisabledStatusWithNoHomeIsHonest(t *testing.T) {
	b := linuxBackend{exe: "/bin/vender", lookPath: func(string) (string, error) { return "", errors.New("nope") }, run: newFakeRun().run}
	st, err := b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Enabled || st.Mechanism != MechanismUnknown {
		t.Errorf("state = %+v", st)
	}
	if !strings.Contains(st.Detail, "$HOME") {
		t.Errorf("detail = %q, want it to name the missing variable", st.Detail)
	}
	if _, err := b.enable(); err == nil {
		t.Error("enable without $HOME succeeded, want an error")
	}
}

func TestSystemdUnitQuotesAPathWithSpaces(t *testing.T) {
	body := systemdUnit(`/home/a b/vender`)
	if !strings.Contains(body, `ExecStart="/home/a b/vender" daemon`) {
		t.Errorf("unit does not quote the path:\n%s", body)
	}
	if got := systemdExec(`/usr/bin/vender`); got != "/usr/bin/vender" {
		t.Errorf("systemdExec left a simple path quoted: %q", got)
	}
	if got := desktopExec(`/home/a b/vender`); got != `"/home/a b/vender"` {
		t.Errorf("desktopExec = %q", got)
	}
}

func TestEnableFailsLoudlyWhenSystemctlFails(t *testing.T) {
	r := newFakeRun()
	b, home := systemdBackend(t, r)
	r.fail["systemctl --user enable --now vender.service"] = true
	r.answers["systemctl --user enable --now vender.service"] = "Failed to enable unit: Access denied"

	_, err := b.enable()
	if err == nil {
		t.Fatal("enable with a failing systemctl succeeded")
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("error = %q, want it to carry systemd's message", err)
	}
	// The unit file is still there: a failed enable is reported, not hidden.
	if _, statErr := os.Stat(filepath.Join(home, ".config", "systemd", "user", "vender.service")); statErr != nil {
		t.Errorf("unit file missing after a failed enable: %v", statErr)
	}
}

// --- TUN mode: the system unit ----------------------------------------------

// tunBackend is the systemd backend with a writable system unit directory and a
// chosen euid, so the TUN path can be driven without root.
func tunBackend(t *testing.T, r *fakeRun, euid int) (linuxBackend, string) {
	t.Helper()
	b, home := systemdBackend(t, r)
	b.systemUnitDir = filepath.Join(home, "etc-systemd-system")
	b.euid = euid
	return b, home
}

// With TUN enabled and root available, `on` installs the *system* unit: it is
// the only mechanism that can carry CAP_NET_ADMIN.
func TestEnableWithTunInstallsASystemUnit(t *testing.T) {
	r := newFakeRun()
	b, home := tunBackend(t, r, 0)

	st, err := b.enableWith(Options{TUN: true})
	if err != nil {
		t.Fatalf("enableWith: %v", err)
	}
	if !st.Enabled || st.Mechanism != MechanismSystemdSystem {
		t.Errorf("state = %+v", st)
	}
	if st.Target != b.systemUnitPath() {
		t.Errorf("target = %q, want %q", st.Target, b.systemUnitPath())
	}

	body, err := os.ReadFile(b.systemUnitPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"User=tester",
		"Environment=HOME=" + home,
		"AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW",
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW",
		"ExecStart=/usr/local/bin/vender daemon",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("system unit is missing %q:\n%s", want, got)
		}
	}
	// The system manager, not the user manager.
	if !r.ran("systemctl daemon-reload") || !r.ran("systemctl enable --now vender.service") {
		t.Errorf("systemctl was not driven for the system manager (calls: %v)", r.calls)
	}
	if r.ran("systemctl --user enable --now vender.service") {
		t.Errorf("the user manager was used for a TUN-enabled registration (calls: %v)", r.calls)
	}
	// A superseded user unit would fight for the run dir's lock.
	if fileExists(filepath.Join(home, ".config", "systemd", "user", "vender.service")) {
		t.Error("a user unit was left next to the system unit")
	}
}

// Without root the system unit cannot be installed. The user unit is still
// registered (the daemon does start at login) and the error says exactly what
// TUN still needs.
func TestEnableWithTunWithoutRootFallsBackAndSaysSo(t *testing.T) {
	r := newFakeRun()
	b, home := tunBackend(t, r, 1000)

	st, err := b.enableWith(Options{TUN: true, CapabilityHint: "sing-box carries CAP_NET_ADMIN (setcap)"})
	if err == nil {
		t.Fatal("enableWith succeeded without root and without a capability")
	}
	if !strings.Contains(err.Error(), "sudo vender autostart on") {
		t.Errorf("error does not name the fix: %v", err)
	}
	if !strings.Contains(err.Error(), "setcap") {
		t.Errorf("error does not quote the capability verdict: %v", err)
	}
	if !st.Enabled || st.Mechanism != MechanismSystemdUser {
		t.Errorf("fallback state = %+v", st)
	}
	if !strings.Contains(st.Detail, "cannot create a TUN") {
		t.Errorf("fallback detail does not explain the TUN problem: %q", st.Detail)
	}
	if !fileExists(filepath.Join(home, ".config", "systemd", "user", "vender.service")) {
		t.Error("the fallback did not install the user unit")
	}
}

// status must tell the truth in all three TUN-relevant states.
func TestStatusWithTun(t *testing.T) {
	t.Run("system unit registered", func(t *testing.T) {
		r := newFakeRun()
		b, _ := tunBackend(t, r, 0)
		r.answers["systemctl is-enabled vender.service"] = "enabled"
		r.answers["systemctl is-active vender.service"] = "active"
		if _, err := b.enableWith(Options{TUN: true}); err != nil {
			t.Fatalf("enableWith: %v", err)
		}
		st, err := b.statusWith(Options{TUN: true})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		if !st.Enabled || st.Mechanism != MechanismSystemdSystem {
			t.Errorf("state = %+v", st)
		}
		for _, want := range []string{"systemctl is-active: active", "can create a TUN"} {
			if !strings.Contains(st.Detail, want) {
				t.Errorf("detail %q is missing %q", st.Detail, want)
			}
		}
	})

	t.Run("user unit registered while TUN is on", func(t *testing.T) {
		r := newFakeRun()
		b, _ := tunBackend(t, r, 1000)
		if _, err := b.enableWith(Options{}); err != nil {
			t.Fatalf("enableWith: %v", err)
		}
		st, err := b.statusWith(Options{TUN: true, CapabilityHint: "the engine needs CAP_NET_ADMIN"})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		if !st.Enabled || st.Mechanism != MechanismSystemdUser {
			t.Errorf("state = %+v", st)
		}
		for _, want := range []string{"cannot create a TUN", "sudo vender autostart on", "CAP_NET_ADMIN"} {
			if !strings.Contains(st.Detail, want) {
				t.Errorf("detail %q is missing %q", st.Detail, want)
			}
		}
	})

	t.Run("nothing registered", func(t *testing.T) {
		r := newFakeRun()
		b, _ := tunBackend(t, r, 1000)
		st, err := b.statusWith(Options{TUN: true})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		if st.Enabled {
			t.Errorf("state = %+v", st)
		}
		if st.Mechanism != MechanismSystemdSystem || !strings.Contains(st.Detail, "system unit") {
			t.Errorf("a disabled TUN status should name the system unit it would install: %+v", st)
		}
	})
}

// `off` removes the system unit too, and refuses to pretend when it cannot: a
// root-owned unit left behind would start the daemon again at the next boot.
func TestDisableRemovesTheSystemUnit(t *testing.T) {
	r := newFakeRun()
	b, _ := tunBackend(t, r, 0)
	if _, err := b.enableWith(Options{TUN: true}); err != nil {
		t.Fatalf("enableWith: %v", err)
	}
	if err := b.disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if fileExists(b.systemUnitPath()) {
		t.Error("the system unit survived disable")
	}
	if !r.ran("systemctl disable --now vender.service") {
		t.Errorf("the system unit was not disabled through systemctl (calls: %v)", r.calls)
	}

	// The same removal as a non-root user has to fail loudly.
	r2 := newFakeRun()
	b2, _ := tunBackend(t, r2, 0)
	if _, err := b2.enableWith(Options{TUN: true}); err != nil {
		t.Fatalf("enableWith: %v", err)
	}
	b2.euid = 1000
	if err := b2.disable(); err == nil {
		t.Error("removing a root-owned system unit as a user succeeded")
	} else if !strings.Contains(err.Error(), "sudo vender autostart off") {
		t.Errorf("error does not name the fix: %v", err)
	}
}

// A system unit that contains a space in the executable path must still be one
// systemd command word, and the paths inside it must survive round-tripping.
func TestSystemUnitQuotesPaths(t *testing.T) {
	got := systemdSystemUnit(`/opt/my vender/vender`, "tester", `/home/my user`)
	if !strings.Contains(got, `ExecStart="/opt/my vender/vender" daemon`) {
		t.Errorf("ExecStart is not quoted:\n%s", got)
	}
	if !strings.Contains(got, `Environment=HOME="/home/my user"`) {
		t.Errorf("HOME is not quoted:\n%s", got)
	}
}

// A TUN registration that is not asked for keeps the old behaviour exactly: the
// user unit, no system unit, no root needed.
func TestEnableWithoutTunStaysUnprivileged(t *testing.T) {
	r := newFakeRun()
	b, _ := tunBackend(t, r, 1000)
	st, err := b.enableWith(Options{})
	if err != nil {
		t.Fatalf("enableWith: %v", err)
	}
	if st.Mechanism != MechanismSystemdUser {
		t.Errorf("mechanism = %q, want the user unit", st.Mechanism)
	}
	if fileExists(b.systemUnitPath()) {
		t.Error("a system unit was installed although TUN is off")
	}
}

// homeOf resolves the invoking user's home for a `sudo vender autostart on`,
// so the system unit points at their configuration rather than root's. It is
// driven through the seam because a test must not need a real account.
func TestHomeOf(t *testing.T) {
	restore := userLookup
	t.Cleanup(func() { userLookup = restore })

	userLookup = func(name string) (*user.User, error) {
		if name != "tester" {
			return nil, errors.New("no such user")
		}
		return &user.User{Username: name, HomeDir: "/home/tester"}, nil
	}
	if got := homeOf("tester"); got != "/home/tester" {
		t.Errorf("homeOf = %q, want /home/tester", got)
	}
	// A missing account is not fatal: the unit simply does not pin HOME.
	if got := homeOf("nobody-here"); got != "" {
		t.Errorf("homeOf for an unknown user = %q, want empty", got)
	}
}

// A system unit that fails to enable must not be left on disk: the fallback
// installs the per-user registration, and a stale system unit beside it would
// make `status` and the next boot disagree.
func TestFailedSystemUnitIsCleanedUp(t *testing.T) {
	r := newFakeRun()
	b, _ := tunBackend(t, r, 0)
	r.fail["systemctl enable --now vender.service"] = true
	r.answers["systemctl enable --now vender.service"] = "Job for vender.service failed"

	st, err := b.enableWith(Options{TUN: true})
	if err == nil {
		t.Fatal("enableWith succeeded although systemctl refused")
	}
	if fileExists(b.systemUnitPath()) {
		t.Error("the system unit was left behind after a failed enable")
	}
	if !st.Enabled || st.Mechanism != MechanismSystemdUser {
		t.Errorf("the fallback state = %+v", st)
	}
}
