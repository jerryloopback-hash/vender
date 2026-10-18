//go:build windows

package core

import (
	"golang.org/x/sys/windows"
	"syscall"
	"unsafe"
)

// TokenElevation is TOKEN_INFORMATION_CLASS's TokenElevation (20); its value is
// a TOKEN_ELEVATION, i.e. a single DWORD that is non-zero when the token is
// elevated.
const (
	tokenQuery     = 0x0008
	tokenElevation = 20
)

// checkTUNPrivilege answers the Windows question: is this process elevated
// enough to create the WinTun adapter sing-box's TUN inbound needs?
//
// It reads the process token's TokenElevation rather than trying the adapter
// and interpreting a failure, because the failure would come from a child
// process that has already been spawned and whose exit reason is easy to
// misread.
//
// The call is deliberately tiny and self-contained: it uses the same
// `syscall.NewLazyDLL` route internal/core already takes for LockFileEx, so it
// adds no dependency. It is the one part of the Windows TUN path that cannot be
// exercised anywhere but Windows.
func checkTUNPrivilege(engineBinary string) TUNPrivilege {
	if isElevated() {
		return TUNPrivilege{OK: true, Detail: "this process runs elevated (Administrator)"}
	}
	return TUNPrivilege{
		OK:     false,
		Detail: "this process is not elevated, and a WinTun adapter needs Administrator rights",
		Hint: "TUN mode needs Administrator on Windows. Pick one:\n" +
			"* start vender from an elevated prompt (right-click, Run as administrator)\n" +
			"* register start-at-login with the highest privileges, once, from an\n" +
			"  elevated prompt:  vender autostart on\n" +
			"    (with TUN enabled this registers the logon task as HighestAvailable,\n" +
			"     so the daemon is elevated at every logon with no further prompts)",
	}
}

// isElevated reports whether the current process token is elevated.
func isElevated() bool {
	process, _, _ := procGetCurrentProcess.Call()
	var token syscall.Handle
	r, _, _ := procOpenProcessToken.Call(
		process,
		uintptr(tokenQuery),
		uintptr(unsafe.Pointer(&token)),
	)
	if r == 0 {
		return false
	}
	defer procCloseHandle.Call(uintptr(token))

	var elevation uint32
	var returned uint32
	r, _, _ = procGetTokenInformation.Call(
		uintptr(token),
		uintptr(tokenElevation),
		uintptr(unsafe.Pointer(&elevation)),
		unsafe.Sizeof(elevation),
		uintptr(unsafe.Pointer(&returned)),
	)
	if r == 0 {
		return false
	}
	return elevation != 0
}

var (
	advapi32 = windows.NewLazySystemDLL("advapi32.dll")

	procOpenProcessToken    = advapi32.NewProc("OpenProcessToken")
	procGetTokenInformation = advapi32.NewProc("GetTokenInformation")

	// kernel32 is declared in lock_windows.go; the process pseudo-handle comes
	// from the same library.
	procGetCurrentProcess = kernel32.NewProc("GetCurrentProcess")
	procCloseHandle       = kernel32.NewProc("CloseHandle")
)
