//go:build windows

package config

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
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
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		s := sd.String()
		if strings.Contains(s, ";;;WD)") || strings.Contains(s, ";;;BU)") || strings.Contains(s, ";;;AU)") {
			t.Fatalf("broad access to %s: %s", path, s)
		}
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s, user.User.Sid.String()) {
			t.Fatalf("missing user access to %s: %s", path, s)
		}
	}
}
