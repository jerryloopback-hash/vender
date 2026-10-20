//go:build windows

package autostart

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestSiblingGUICompanionPEValidation(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "vender.exe")
	if got, err := siblingGUICompanion(cli); err != nil || got != "" {
		t.Fatal("missing helper must permit explicit console fallback")
	}
	helper := filepath.Join(dir, "vender-daemon.exe")
	if err := os.WriteFile(helper, []byte("not a PE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := siblingGUICompanion(cli); err == nil {
		t.Fatal("invalid companion accepted")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	offset := int(binary.LittleEndian.Uint32(b[0x3c:])) + 24 + 68
	// These are disposable copies of this test fixture, never installed binaries.
	binary.LittleEndian.PutUint16(b[offset:], 3)
	if err := os.WriteFile(helper, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := siblingGUICompanion(cli); err == nil {
		t.Fatal("console-subsystem companion accepted")
	}
	binary.LittleEndian.PutUint16(b[offset:], 2)
	if err := os.WriteFile(helper, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := siblingGUICompanion(cli); err != nil || got != helper {
		t.Fatalf("GUI companion not selected: %s %v", got, err)
	}
}
