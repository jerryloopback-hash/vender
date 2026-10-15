package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunIsNotImplementedUntilM3(t *testing.T) {
	err := Run(context.Background(), Options{ConfigDir: "/tmp/vender"})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Run() = %v, want ErrNotImplemented", err)
	}
	if !strings.Contains(err.Error(), "(M3)") {
		t.Errorf("error = %q, want it to name the milestone", err)
	}
}
