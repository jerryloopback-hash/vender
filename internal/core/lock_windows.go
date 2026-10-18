//go:build windows

package core

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"syscall"
	"unsafe"
)

// Windows has no flock; the equivalent is LockFileEx with
// LOCKFILE_FAIL_IMMEDIATELY, which fails at once instead of queueing. It is
// released when the handle is closed, and the OS closes every handle when the
// process dies, so the lock still cannot go stale.
var (
	// syscall.NewLazyDLL resolves "kernel32.dll" with
	// LOAD_LIBRARY_SEARCH_SYSTEM32, so a rogue copy in the working directory
	// cannot be loaded.
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// errWouldBlock mirrors the unix name; on Windows a contended LockFileEx
// reports ERROR_LOCK_VIOLATION.
var errWouldBlock = syscall.Errno(33) // ERROR_LOCK_VIOLATION

func tryLock(f *os.File) error {
	var ol syscall.Overlapped
	r1, _, err := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1, // lock one byte
		0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		if err == nil || errors.Is(err, syscall.Errno(0)) {
			return errWouldBlock
		}
		return err
	}
	return nil
}

func unlock(f *os.File) error {
	var ol syscall.Overlapped
	r1, _, err := procUnlockFileEx.Call(
		f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		if err == nil || errors.Is(err, syscall.Errno(0)) {
			return nil
		}
		return err
	}
	return nil
}
