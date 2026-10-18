//go:build windows

package config

import (
	"path/filepath"
	"testing"

	"github.com/ll-lite/vender/internal/testutil/windowsacl"
)

// Verify actual Store writes, not POSIX mode bits (which do not express NTFS
// privacy). Other OSes retain their existing chmod assertions.
func TestStoreWritesPrivateWindowsFiles(t *testing.T) {
	store := NewStoreAt(filepath.Join(t.TempDir(), "private-config"))
	if err := store.SaveConfig(Default()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServers(NewServerSet()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfiles(NewProfileSet()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.ConfigPath(), store.ServersPath(), store.ProfilesPath()} {
		windowsacl.AssertPrivate(t, path)
	}
}
