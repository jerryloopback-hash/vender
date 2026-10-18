package core

import (
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
)

// freePort asks the kernel for an unused TCP port and releases it again. The
// window between releasing and rebinding is racy in principle; in practice the
// kernel does not hand the same ephemeral port to another process immediately,
// and the alternative (a fixed port) makes the test suite unparallelizable.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeEngineRuntime builds a RuntimeConfig whose BinaryPath is this test
// binary, which TestMain turns into a stand-in sing-box.
func fakeEngineRuntime(t *testing.T, mode string, mutate func(*config.Config)) RuntimeConfig {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv(fakeEngineEnv, mode)

	cfg := *config.Default()
	cfg.Proxy.Listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(t)))
	if mutate != nil {
		mutate(&cfg)
	}
	return RuntimeConfig{
		Config:     cfg,
		Server:     testVLESS(),
		BinaryPath: exe,
		RunDir:     t.TempDir(),
	}
}

// fastEngine shrinks the supervision timings so the lifecycle tests run in
// milliseconds rather than tens of seconds.
func fastEngine(t *testing.T, rc RuntimeConfig) *engine {
	t.Helper()
	eng, err := New(rc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e := eng.(*engine)
	e.readyTimeout = 3 * time.Second
	e.pollInterval = 10 * time.Millisecond
	e.dialTimeout = 200 * time.Millisecond
	e.stopGrace = 3 * time.Second
	return e
}

func TestEngineLifecycle(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)

	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	// The generated config exists, is private, and is real JSON.
	cfgPath := ConfigPath(rc.RunDir)
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("generated config missing: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("generated config mode = %o, want 600 (it holds credentials)", perm)
	}

	start := time.Now()
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Start took %s, want it to return as soon as the listener is up", elapsed)
	}

	if pid := e.Pid(); pid <= 0 {
		t.Errorf("Pid() = %d after Start, want a live pid", pid)
	}
	if !e.Healthy() {
		t.Error("Healthy() = false after Start, want true")
	}

	// The engine's own output reaches Logs().
	if !waitForLog(t, e.Logs(), "server started at", 3*time.Second) {
		t.Error("Logs() never carried the listener announcement")
	}

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if e.Healthy() {
		t.Error("Healthy() = true after Stop, want false")
	}
	if err := e.Stop(); err != nil {
		t.Errorf("second Stop() = %v, want nil (idempotent)", err)
	}

	// Logs() must be closed once the process is gone, so a consumer ranging
	// over it terminates instead of leaking a goroutine.
	if !channelClosed(e.Logs(), 3*time.Second) {
		t.Error("Logs() channel was not closed after Stop")
	}

	// The port is free again, so a restart works.
	ln, err := net.Listen("tcp", rc.Config.Proxy.Listen)
	if err != nil {
		t.Fatalf("port %s was not released: %v", rc.Config.Proxy.Listen, err)
	}
	ln.Close()
}

func TestEngineRestart(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := e.Start(); err != nil {
			t.Fatalf("Start #%d: %v", i+1, err)
		}
		if !e.Healthy() {
			t.Fatalf("Healthy() = false after Start #%d", i+1)
		}
		if err := e.Stop(); err != nil {
			t.Fatalf("Stop #%d: %v", i+1, err)
		}
	}
}

func TestStartWithoutWriteConfigFails(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)
	err := e.Start()
	if err == nil {
		_ = e.Stop()
		t.Fatal("Start without WriteConfig = nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "WriteConfig") {
		t.Errorf("error = %q, want it to point at WriteConfig", err)
	}
}

func TestStartFailsWhenTheEngineDiesImmediately(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeFail, nil)
	e := fastEngine(t, rc)
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	err := e.Start()
	if err == nil {
		_ = e.Stop()
		t.Fatal("Start = nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "exited before it began listening") {
		t.Errorf("error = %q, want it to explain that the engine exited", err)
	}
	// The engine's own diagnosis must be quoted back to the user.
	if !strings.Contains(err.Error(), "deliberate failure") {
		t.Errorf("error = %q, want it to include the engine's output", err)
	}
	if e.Healthy() {
		t.Error("Healthy() = true after a failed start, want false")
	}
}

func TestStartTimesOutWhenThePortNeverOpens(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeNeverListen, nil)
	e := fastEngine(t, rc)
	e.readyTimeout = 300 * time.Millisecond
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	err := e.Start()
	if err == nil {
		_ = e.Stop()
		t.Fatal("Start = nil error, want a readiness timeout")
	}
	if !strings.Contains(err.Error(), "did not start listening") {
		t.Errorf("error = %q, want a readiness timeout", err)
	}
	// A failed Start must not leave the process running.
	if e.Pid() != 0 && processAlive(e.Pid()) {
		t.Errorf("pid %d is still alive after a failed Start", e.Pid())
	}
}

func TestStartFailsWhenTheListenPortIsTaken(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)

	// Something else already owns the port (a stray sing-box, another proxy).
	ln, err := net.Listen("tcp", rc.Config.Proxy.Listen)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	err = e.Start()
	if err == nil {
		_ = e.Stop()
		t.Fatal("Start with an occupied port = nil error, want a failure")
	}
	if !strings.Contains(err.Error(), "already accepting connections") {
		t.Errorf("error = %q, want it to explain the occupied port", err)
	}
	if !strings.Contains(err.Error(), rc.Config.Proxy.Listen) {
		t.Errorf("error = %q, want it to name the address", err)
	}
}

// A caller may subscribe before Start (a daemon that wires its log pump up
// first); the run's output must still reach it.
func TestLogsSubscribedBeforeStartStillReceivesOutput(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	logs := e.Logs()
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !waitForLog(t, logs, "server started at", 3*time.Second) {
		t.Error("a subscriber that called Logs() before Start saw no output")
	}
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestHealthyGoesFalseWhenTheEngineExits(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeExitAfter+"150", nil)
	e := fastEngine(t, rc)
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !e.Healthy() {
		t.Fatal("Healthy() = false right after Start")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !e.Healthy() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("Healthy() stayed true although the engine exited")
}

func TestWriteConfigRejectsInvalidRuntimeConfig(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)
	bad := rc
	bad.RunDir = ""
	if err := e.WriteConfig(bad); err == nil {
		t.Fatal("WriteConfig with no run dir = nil error, want a failure")
	}
}

func TestWriteConfigIsAtomicAndOverwrites(t *testing.T) {
	rc := fakeEngineRuntime(t, fakeOK, nil)
	e := fastEngine(t, rc)
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	first, err := os.ReadFile(ConfigPath(rc.RunDir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	rc.Config.LogLevel = config.LogLevelDebug
	if err := e.WriteConfig(rc); err != nil {
		t.Fatalf("second WriteConfig: %v", err)
	}
	second, err := os.ReadFile(ConfigPath(rc.RunDir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(first) == string(second) {
		t.Error("WriteConfig did not pick up the changed log level")
	}
	if !strings.Contains(string(second), `"debug"`) {
		t.Errorf("rewritten config = %s, want log level debug", second)
	}
	// No temp files may be left behind.
	entries, err := os.ReadDir(rc.RunDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".") {
			t.Errorf("leftover temp file in the run dir: %s", ent.Name())
		}
	}
}

// --- helpers ---------------------------------------------------------------

func waitForLog(t *testing.T, logs <-chan string, substr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-logs:
			if !ok {
				return false
			}
			if strings.Contains(line, substr) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func channelClosed(ch <-chan string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 is the POSIX liveness probe: it is delivered but ignored, so it
	// succeeds only while the process exists. On Windows os.Process.Signal
	// rejects signals other than Kill, so this helper conservatively reports
	// false there and callers skip their assertion.
	return p.Signal(syscall.Signal(0)) == nil
}
