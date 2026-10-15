package autostart

import (
	"errors"
	"strings"
	"testing"
)

func TestEntryPointsAreNotImplementedUntilM4(t *testing.T) {
	if err := Enable(); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Enable() = %v, want ErrNotImplemented", err)
	}
	if err := Disable(); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Disable() = %v, want ErrNotImplemented", err)
	}
	state, err := Status()
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Status() error = %v, want ErrNotImplemented", err)
	}
	if state.Enabled {
		t.Errorf("Status() = %#v, want Enabled=false", state)
	}
	if state.Mechanism != MechanismUnknown {
		t.Errorf("Mechanism = %q, want %q", state.Mechanism, MechanismUnknown)
	}
	if !strings.Contains(err.Error(), "(M4)") {
		t.Errorf("error = %q, want it to name the milestone", err)
	}
}
