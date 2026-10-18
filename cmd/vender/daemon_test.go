package main

import (
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
)

// These tests exercise the daemon/status/proxy wiring without a real sing-box:
// an advisory lock or an unrelated TCP listener must not prove engine health.

// holdLock takes the run dir's lock for the duration of the test, standing in
// for a running daemon.
func holdLock(t *testing.T, runDir string) {
	t.Helper()
	lock, err := core.AcquireLock(core.LockPath(runDir))
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
}

func TestCLIStatusFailsWhenNoDaemonRuns(t *testing.T) {
	cfgDir := t.TempDir()
	runDir := t.TempDir()
	// Pin the listen address to a port nothing is using: the default (7897) is
	// the conventional clash mixed port, and on a developer's machine a real
	// proxy (or vender itself) may legitimately be listening on it, which would
	// make this "nothing is accepting" assertion depend on the host.
	writeConfig(t, cfgDir, "127.0.0.1:"+strconv.Itoa(freePort(t)))

	out, err := runCLIIn(t, runDir, "status", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("status without a daemon = nil error, want a failure (output: %q)", out)
	}
	if !errors.Is(err, errNotRunning) {
		t.Errorf("error = %v, want it to wrap errNotRunning", err)
	}
	for _, want := range []string{"state        stopped", "run dir      " + runDir, "not accepting connections"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output = %q, want it to contain %q", out, want)
		}
	}
}

func TestCLIStatusFailsWhenTheDaemonIsUpButThePortIsClosed(t *testing.T) {
	cfgDir := t.TempDir()
	runDir := t.TempDir()
	holdLock(t, runDir)

	// A port nothing listens on.
	closed := freePort(t)
	writeConfig(t, cfgDir, "127.0.0.1:"+strconv.Itoa(closed))

	out, err := runCLIIn(t, runDir, "status", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("status = nil error, want a failure (output: %q)", out)
	}
	if errors.Is(err, errNotRunning) {
		t.Errorf("error = %v, want it to distinguish \"running but not listening\"", err)
	}
	if !strings.Contains(err.Error(), "control channel") {
		t.Errorf("error = %q, want it to explain the unverified engine state", err)
	}
	if !strings.Contains(out, "state        running") {
		t.Errorf("status output = %q, want it to report running", out)
	}
}

func TestCLIStatusRejectsALockAndUnrelatedListener(t *testing.T) {
	cfgDir := t.TempDir()
	runDir := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	writeConfig(t, cfgDir, ln.Addr().String())

	holdLock(t, runDir)
	if err := core.WritePid(runDir); err != nil {
		t.Fatalf("WritePid: %v", err)
	}

	out, err := runCLIIn(t, runDir, "status", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("status = nil error despite having no authenticated daemon (output: %q)", out)
	}
	for _, want := range []string{"state        running", "pid          "} {
		if !strings.Contains(out, want) {
			t.Errorf("status output = %q, want it to contain %q", out, want)
		}
	}
	if !strings.Contains(err.Error(), "control channel") {
		t.Errorf("error = %v, want an unverified engine error", err)
	}
}

func TestCLIDaemonRefusesASecondInstance(t *testing.T) {
	cfgDir := t.TempDir()
	runDir := t.TempDir()

	if out, err := runCLIIn(t, runDir, "import", vlessLink, "--config-dir", cfgDir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	holdLock(t, runDir)

	out, err := runCLIIn(t, runDir, "daemon", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("second daemon = nil error, want a lock failure (output: %q)", out)
	}
	if !errors.Is(err, core.ErrLocked) {
		t.Errorf("error = %v, want it to wrap core.ErrLocked", err)
	}
}

func TestCLIDaemonRequiresASelectedServer(t *testing.T) {
	cfgDir := t.TempDir()
	out, err := runCLIIn(t, t.TempDir(), "daemon", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("daemon with no server = nil error, want a failure (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "no server selected") {
		t.Errorf("error = %q, want it to explain the missing selection", err)
	}
	if !strings.Contains(err.Error(), "vender import") {
		t.Errorf("error = %q, want it to suggest the import command", err)
	}
}

func TestCLIDaemonReportsAMissingBinary(t *testing.T) {
	cfgDir := t.TempDir()
	runDir := t.TempDir()
	if out, err := runCLIIn(t, runDir, "import", vlessLink, "--config-dir", cfgDir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	// Point engine.binary at a path that does not exist and empty PATH so the
	// "bundled"/PATH steps cannot rescue it either.
	cfg := *config.Default()
	cfg.SelectedServer = "Test-VLESS"
	cfg.Engine.Binary = filepath.Join(t.TempDir(), "no-such-sing-box")
	if err := config.NewStoreAt(cfgDir).SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	t.Setenv("PATH", "")

	out, err := runCLIIn(t, runDir, "daemon", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("daemon without sing-box = nil error, want a failure (output: %q)", out)
	}
	if !errors.Is(err, core.ErrBinaryNotFound) {
		t.Errorf("error = %v, want it to wrap core.ErrBinaryNotFound", err)
	}
	for _, want := range []string{"engine.binary", "next to the vender executable", "on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	// The lock must be released again after a failed start.
	running, _, lerr := daemonRunning(runDir)
	if lerr != nil {
		t.Fatalf("daemonRunning: %v", lerr)
	}
	if running {
		t.Error("run dir is still locked after the daemon failed to start")
	}
}

// The proxy command must never touch the host's real desktop settings during a
// test: the platform implementation is swapped for a recorder.
func TestCLIProxyOnOff(t *testing.T) {
	if !systemProxySupported() {
		t.Skip("no system proxy support on this platform")
	}
	type call struct {
		on     bool
		listen string
	}
	var calls []call
	restore := applySystemProxy
	applySystemProxy = func(on bool, listen string) error {
		calls = append(calls, call{on, listen})
		return nil
	}
	t.Cleanup(func() { applySystemProxy = restore })

	cfgDir := t.TempDir()
	runDir := t.TempDir()
	writeConfig(t, cfgDir, "127.0.0.1:9999")

	out, err := runCLIIn(t, runDir, "proxy", "on", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("proxy on: %v\n%s", err, out)
	}
	for _, want := range []string{"System proxy set to 127.0.0.1:9999", "export http_proxy=http://127.0.0.1:9999", "export all_proxy=socks5://127.0.0.1:9999", "no daemon is running"} {
		if !strings.Contains(out, want) {
			t.Errorf("proxy on output = %q, want it to contain %q", out, want)
		}
	}

	out, err = runCLIIn(t, runDir, "proxy", "off", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("proxy off: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restored") && !strings.Contains(out, "disabled") {
		t.Errorf("proxy off output = %q", out)
	}

	if len(calls) != 2 || !calls[0].on || calls[1].on {
		t.Fatalf("applySystemProxy calls = %+v, want [on, off]", calls)
	}
	if calls[0].listen != "127.0.0.1:9999" {
		t.Errorf("applySystemProxy listen = %q, want the configured address", calls[0].listen)
	}
}

func TestCLIProxyReportsAFailure(t *testing.T) {
	if !systemProxySupported() {
		t.Skip("no system proxy support on this platform")
	}
	restore := applySystemProxy
	applySystemProxy = func(bool, string) error { return errors.New("gsettings exploded") }
	t.Cleanup(func() { applySystemProxy = restore })

	out, err := runCLI(t, "proxy", "on", "--config-dir", t.TempDir())
	if err == nil {
		t.Fatalf("proxy on = nil error, want the injected failure (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "gsettings exploded") {
		t.Errorf("error = %q, want it to carry the cause", err)
	}
}

// --- helpers ---------------------------------------------------------------

func writeConfig(t *testing.T, cfgDir, listen string) {
	t.Helper()
	cfg := *config.Default()
	cfg.Proxy.Listen = listen
	if err := config.NewStoreAt(cfgDir).SaveConfig(&cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
