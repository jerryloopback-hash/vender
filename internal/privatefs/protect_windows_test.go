//go:build windows

package privatefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateWindowsACLAndInheritedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "中文 private")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "token.json")
	if err := os.WriteFile(path, []byte("dummy-test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, path} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		text := sd.String()
		if strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;BU)") || strings.Contains(text, ";;;AU)") {
			t.Fatalf("broad access in DACL: %s", text)
		}
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, user.User.Sid.String()) || !strings.Contains(text, ";;;SY)") {
			t.Fatalf("missing user/SYSTEM access: %s", text)
		}
	}
	if err := Protect(path); err != nil {
		t.Fatal(err)
	}
	if err := Protect(path); err != nil {
		t.Fatal(err)
	}
}
