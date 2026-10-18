package control

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/core"
)

func awaitController(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for controller state")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUnexpectedExitRecoversButManualStopDoesNot(t *testing.T) {
	f := newFixture(t)
	f.ctrl.recoveryDelay = 10 * time.Millisecond
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	// The fake's Stop simulates the child's log EOF without calling the
	// controller's explicit StopEngine operation.
	if err := f.engine.Stop(); err != nil {
		t.Fatal(err)
	}
	awaitController(t, func() bool {
		starts, _, running := f.engine.state()
		return starts == 2 && running && f.ctrl.Status().EngineRunning
	})
	if err := f.ctrl.StopEngine(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	starts, _, running := f.engine.state()
	if starts != 2 || running {
		t.Fatalf("manual stop was undone: starts=%d running=%v", starts, running)
	}
}

func TestRecoveryFailureBudgetIsBounded(t *testing.T) {
	f := newFixture(t)
	f.ctrl.recoveryDelay = time.Millisecond
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	f.engine.mu.Lock()
	f.engine.startErr = errors.New("persistent startup failure")
	f.engine.mu.Unlock()
	_ = f.engine.Stop()
	awaitController(t, func() bool { return strings.Contains(f.ctrl.Status().LastError, "recovery limit reached") })
	f.ctrl.mu.RLock()
	attempts := len(f.ctrl.recoveryAttempts)
	f.ctrl.mu.RUnlock()
	if attempts != recoveryLimit {
		t.Fatalf("attempts=%d, want %d", attempts, recoveryLimit)
	}
}

func TestShortLivedSuccessfulRestartsAlsoConsumeBudget(t *testing.T) {
	f := newFixture(t)
	f.ctrl.recoveryDelay = time.Millisecond
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < recoveryLimit; i++ {
		_ = f.engine.Stop()
		want := i + 2
		awaitController(t, func() bool {
			starts, _, running := f.engine.state()
			return starts == want && running && f.ctrl.Status().EngineRunning
		})
	}
	_ = f.engine.Stop()
	awaitController(t, func() bool { return strings.Contains(f.ctrl.Status().LastError, "recovery limit reached") })
}

func TestManualStopCancelsPendingRecovery(t *testing.T) {
	f := newFixture(t)
	f.ctrl.recoveryDelay = 50 * time.Millisecond
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	_ = f.engine.Stop()
	awaitController(t, func() bool { return !f.ctrl.Status().EngineRunning })
	if err := f.ctrl.StopEngine(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	starts, _, running := f.engine.state()
	if starts != 1 || running {
		t.Fatalf("pending recovery ignored manual stop: starts=%d running=%v", starts, running)
	}
}

func TestRestartRejectsInvalidDiskConfigBeforeStopping(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.store.ConfigPath(), []byte("proxy:\n  mode: invalid-mode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.RestartEngine(); err == nil {
		t.Fatal("invalid restart succeeded")
	}
	_, stops, running := f.engine.state()
	if stops != 0 || !running || !f.ctrl.Status().EngineHealthy {
		t.Fatalf("bad config stopped the working engine: stops=%d running=%v", stops, running)
	}
}

type candidateEngine struct {
	*fakeEngine
	rejectCandidate bool
	failNextStart   bool
}

func (e *candidateEngine) CheckConfig(core.RuntimeConfig) error {
	if e.rejectCandidate {
		return errors.New("engine rejects candidate")
	}
	return nil
}

func (e *candidateEngine) Start() error {
	if e.failNextStart {
		e.failNextStart = false
		return errors.New("candidate could not bind")
	}
	return e.fakeEngine.Start()
}

func TestRestartChecksRealEngineBeforeStopping(t *testing.T) {
	f := newFixture(t)
	e := &candidateEngine{fakeEngine: f.engine}
	f.ctrl.engine = e
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	e.rejectCandidate = true
	if err := f.ctrl.SetMode("global"); err == nil {
		t.Fatal("rejected candidate was applied")
	}
	_, stops, running := f.engine.state()
	if stops != 0 || !running || f.ctrl.Status().Mode != "rule" {
		t.Fatal("candidate rejection changed the applied state")
	}
}

func TestFailedStartRestoresPreviousEngine(t *testing.T) {
	f := newFixture(t)
	e := &candidateEngine{fakeEngine: f.engine}
	f.ctrl.engine = e
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	e.failNextStart = true
	if err := f.ctrl.SetMode("global"); err == nil || !strings.Contains(err.Error(), "previous engine restored") {
		t.Fatalf("missing rollback verdict: %v", err)
	}
	st := f.ctrl.Status()
	if !st.EngineHealthy || st.Mode != "rule" || f.engine.lastWrite().Config.Proxy.Mode != "rule" {
		t.Fatalf("previous engine was not restored: %+v", st)
	}
}

func TestRecoveryUsesLastAppliedConfigNotBrokenDiskEdits(t *testing.T) {
	f := newFixture(t)
	f.ctrl.recoveryDelay = time.Millisecond
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.store.ConfigPath(), []byte("invalid: [yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = f.engine.Stop()
	awaitController(t, func() bool {
		starts, _, running := f.engine.state()
		return starts == 2 && running && f.ctrl.Status().EngineRunning
	})
}
