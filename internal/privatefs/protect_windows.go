//go:build windows

package privatefs

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Chmod(0600) on Windows does not set a private ACL. Grant the current account
// (same SID in elevated and unelevated sessions) and SYSTEM full access only.
// Inheritable entries protect newly created temp files before atomic rename.
func Protect(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect private path %s: %w", path, err)
	}
	return nil
}
