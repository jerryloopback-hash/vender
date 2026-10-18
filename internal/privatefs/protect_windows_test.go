//go:build windows

package privatefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ll-lite/vender/internal/testutil/windowsacl"
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
		windowsacl.AssertPrivate(t, p)
	}
	for i := 0; i < 2; i++ {
		if err := Protect(path); err != nil {
			t.Fatal(err)
		}
		windowsacl.AssertPrivate(t, path)
	}
}
