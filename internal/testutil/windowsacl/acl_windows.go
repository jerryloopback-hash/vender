//go:build windows

// Package windowsacl provides assertions shared by Windows filesystem tests.
package windowsacl

import (
	"fmt"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AssertPrivate checks the actual DACL, independently of SDDL's choice to
// abbreviate well-known SIDs (including the local Administrator as LA).
func AssertPrivate(t testing.TB, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDACL(sd, user.User.Sid, system); err != nil {
		t.Fatalf("private ACL for %s: %v (SDDL: %s)", path, err, sd.String())
	}
}

func checkDACL(sd *windows.SECURITY_DESCRIPTOR, user, system *windows.SID) error {
	// GetAce and SidStart point into this descriptor's backing allocation.
	defer runtime.KeepAlive(sd)
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("get DACL: %w", err)
	}
	if dacl == nil {
		return fmt.Errorf("null DACL grants unrestricted access")
	}
	var foundUser, foundSystem bool
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("get ACE %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("ACE %d has unexpected type %d", i, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		isUser, isSystem := sid.Equals(user), sid.Equals(system)
		if !isUser && !isSystem {
			return fmt.Errorf("ACE %d grants access to unexpected SID %s", i, sid.String())
		}
		// FILE_ALL_ACCESS: standard required rights, synchronize, and all
		// nine file-specific rights. An inherit-only ACE grants no access
		// to the object being checked.
		const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
		if ace.Mask != fullAccess || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			return fmt.Errorf("ACE %d lacks effective full file access (mask %#x, flags %#x)", i, ace.Mask, ace.Header.AceFlags)
		}
		foundUser = foundUser || isUser
		foundSystem = foundSystem || isSystem
	}
	if !foundUser || !foundSystem {
		return fmt.Errorf("missing current user or SYSTEM access (user=%v, SYSTEM=%v)", foundUser, foundSystem)
	}
	return nil
}
