//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// systemProxySupported reports whether `vender proxy on|off` can change the
// desktop's proxy settings on this platform.
func systemProxySupported() bool { return true }

// setSystemProxy writes the per-user WinINET proxy and refreshes it, or turns
// it off. See winproxy.go for the key layout and the ordering; everything here
// is the Windows API surface.
func setSystemProxy(on bool, listen string) error {
	return applyWindowsProxy(winReg{}, winInetRefresh, on, listen)
}

// The DLLs are resolved with syscall.NewLazyDLL, which searches System32 only,
// so a rogue DLL in the working directory cannot be loaded. This is the same
// approach internal/core uses for LockFileEx.
var (
	advapi32DLL = syscall.NewLazyDLL("advapi32.dll")
	wininetDLL  = syscall.NewLazyDLL("wininet.dll")

	procRegCreateKeyExW    = advapi32DLL.NewProc("RegCreateKeyExW")
	procRegSetValueExW     = advapi32DLL.NewProc("RegSetValueExW")
	procRegCloseKey        = advapi32DLL.NewProc("RegCloseKey")
	procInternetSetOptionW = wininetDLL.NewProc("InternetSetOptionW")
)

const (
	hkeyCurrentUser = syscall.HKEY_CURRENT_USER
	keySetValue     = syscall.KEY_SET_VALUE
	regSZ           = syscall.REG_SZ
	regDWORD        = syscall.REG_DWORD
)

// winReg is the winRegistry implementation: HKEY_CURRENT_USER only, so no
// elevation is needed and no machine-wide setting is touched.
type winReg struct{}

func (winReg) SetString(subkey, name, value string) error {
	data := syscall.StringToUTF16(value) // NUL-terminated
	return setWinValue(subkey, name, regSZ, unsafe.Pointer(&data[0]), uint32(len(data)*2))
}

func (winReg) SetDWORD(subkey, name string, value uint32) error {
	v := value
	return setWinValue(subkey, name, regDWORD, unsafe.Pointer(&v), 4)
}

func setWinValue(subkey, name string, typ uint32, data unsafe.Pointer, size uint32) error {
	sub, err := syscall.UTF16PtrFromString(subkey)
	if err != nil {
		return fmt.Errorf("subkey %q: %w", subkey, err)
	}
	val, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("value name %q: %w", name, err)
	}

	var h syscall.Handle
	// Create (not just open) so a freshly provisioned profile that has never
	// run an Internet program still gets the key instead of a not-found error.
	r1, _, callErr := procRegCreateKeyExW.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(sub)),
		0, // Reserved
		0, // lpClass
		0, // REG_OPTION_NON_VOLATILE
		uintptr(keySetValue),
		0, // lpSecurityAttributes
		uintptr(unsafe.Pointer(&h)),
		0, // lpdwDisposition
	)
	if r1 != 0 {
		return fmt.Errorf("open HKCU\\%s: %w", subkey, winCallErr(callErr))
	}
	defer procRegCloseKey.Call(uintptr(h))

	r1, _, callErr = procRegSetValueExW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(val)),
		0, // Reserved
		uintptr(typ),
		uintptr(data),
		uintptr(size),
	)
	if r1 != 0 {
		return fmt.Errorf("set %s: %w", name, winCallErr(callErr))
	}
	return nil
}

const (
	internetOptionRefresh         = 37 // INTERNET_OPTION_REFRESH
	internetOptionSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
)

// winInetRefresh tells WinINET the registry changed and makes it re-read the
// configuration. Both calls are required: SETTINGS_CHANGED invalidates the
// cached settings, REFRESH propagates them to the processes that use them.
// Writing ProxyEnable without this leaves already-running applications on the
// old configuration.
func winInetRefresh() error {
	for _, option := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		r1, _, callErr := procInternetSetOptionW.Call(0, option, 0, 0)
		if r1 == 0 {
			return fmt.Errorf("InternetSetOption(%d): %w", option, winCallErr(callErr))
		}
	}
	return nil
}

// winCallErr turns a LazyProc.Call error into something worth printing: the
// syscall package reports a zero Errno when the API failed without setting
// GetLastError.
func winCallErr(err error) error {
	if errno, ok := err.(syscall.Errno); ok {
		if errno == 0 {
			return errors.New("the call failed")
		}
		return errno
	}
	if err == nil {
		return errors.New("the call failed")
	}
	return err
}
