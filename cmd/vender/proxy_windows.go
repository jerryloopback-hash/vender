//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/ll-lite/vender/internal/core"
	"github.com/ll-lite/vender/internal/privatefs"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func systemProxySupported() bool { return true }

func setSystemProxy(on bool, listen string) error { return setWindowsProxy(on, listen, false) }

func forceRestoreSystemProxy() error { return setWindowsProxy(false, "", true) }

func setWindowsProxy(on bool, listen string, force bool) error {
	// The OS setting is per user, not per vender profile or run directory.
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "vender")
	if err := privatefs.EnsureDir(dir); err != nil {
		return err
	}
	lock, err := core.AcquireLock(filepath.Join(dir, "system-proxy.lock"))
	if err != nil {
		return fmt.Errorf("another system proxy operation may be running: %w", err)
	}
	defer lock.Release()
	return applyWindowsProxy(winInetAPI{}, winProxyStore{}, on, listen, force)
}

var (
	wininetDLL               = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOptionW   = wininetDLL.NewProc("InternetSetOptionW")
	procInternetQueryOptionW = wininetDLL.NewProc("InternetQueryOptionW")
	procGlobalFree           = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
	procRegFlushKey          = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegFlushKey")
)

const (
	optionPerConnection   = 75
	optionRefresh         = 37
	optionSettingsChanged = 39
	backupKey             = `Software\vender`
	backupValue           = "SystemProxyBackupV1"
)

// The union includes a FILETIME (8 bytes), even on 32-bit Windows. Using a
// uint64 gives the correct native union size/alignment on our Windows targets.
type perConnOption struct {
	Option uint32
	Value  uint64
}
type perConnList struct {
	Size       uint32
	Connection *uint16 // nil selects the LAN settings, not dial-up profiles
	Count      uint32
	Error      uint32
	Options    *perConnOption
}

type winInetAPI struct{}

func (winInetAPI) Read() (winProxySettings, error) {
	options := []perConnOption{{Option: 1}, {Option: 2}, {Option: 3}, {Option: 4}}
	list := perConnList{Count: uint32(len(options)), Options: &options[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	size := list.Size
	ok, _, err := procInternetQueryOptionW.Call(0, optionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	// WinINET allocates returned string buffers with GlobalAlloc.
	defer func() {
		for _, o := range options[1:] {
			if o.Value != 0 {
				_, _, _ = procGlobalFree.Call(uintptr(o.Value))
			}
		}
	}()
	if ok == 0 {
		return winProxySettings{}, fmt.Errorf("InternetQueryOption: %w", err)
	}
	text := func(option *perConnOption) string {
		if option.Value == 0 {
			return ""
		}
		// Read the pointer member of the native union directly rather than
		// round-tripping an address through a Go integer.
		return windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&option.Value)))
	}
	return winProxySettings{Flags: uint32(options[0].Value), Server: text(&options[1]), Bypass: text(&options[2]), AutoConfigURL: text(&options[3])}, nil
}

func (winInetAPI) Write(settings winProxySettings) error {
	values := make([]*uint16, 3)
	for i, s := range []string{settings.Server, settings.Bypass, settings.AutoConfigURL} {
		p, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return err
		}
		values[i] = p
	}
	options := []perConnOption{{Option: 1, Value: uint64(settings.Flags)},
		{Option: 2, Value: uint64(uintptr(unsafe.Pointer(values[0])))},
		{Option: 3, Value: uint64(uintptr(unsafe.Pointer(values[1])))},
		{Option: 4, Value: uint64(uintptr(unsafe.Pointer(values[2])))}}
	list := perConnList{Count: uint32(len(options)), Options: &options[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	ok, _, err := procInternetSetOptionW.Call(0, optionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(list.Size))
	runtime.KeepAlive(values)
	if ok == 0 {
		return fmt.Errorf("InternetSetOption(per-connection): %w", err)
	}
	return nil
}

func (winInetAPI) Refresh() error {
	for _, option := range []uintptr{optionSettingsChanged, optionRefresh} {
		ok, _, err := procInternetSetOptionW.Call(0, option, 0, 0)
		if ok == 0 {
			return fmt.Errorf("InternetSetOption(%d): %w", option, err)
		}
	}
	return nil
}

type winProxyStore struct{}

func (winProxyStore) Load() (*winProxyBackup, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, backupKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer key.Close()
	data, _, err := key.GetStringValue(backupValue)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var backup winProxyBackup
	if err := json.Unmarshal([]byte(data), &backup); err != nil {
		return nil, fmt.Errorf("invalid saved proxy state: %w", err)
	}
	return &backup, nil
}
func (winProxyStore) Save(backup winProxyBackup) error {
	data, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, backupKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue(backupValue, string(data)); err != nil {
		return err
	}
	return flushProxyKey(key)
}
func (winProxyStore) Clear() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, backupKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(backupValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return flushProxyKey(key)
}

func flushProxyKey(key registry.Key) error {
	status, _, _ := procRegFlushKey.Call(uintptr(key))
	if status != 0 {
		return windows.Errno(status)
	}
	return nil
}
