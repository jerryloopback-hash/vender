//go:build windows

package main

import "errors"

// systemProxySupported reports whether `vender proxy on|off` can change the
// desktop's proxy settings on this platform.
//
// Windows stores the per-user WinINET proxy in
// HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings and needs a
// WinINET/WinHTTP refresh call after writing it; that lands with the autostart
// milestone (M4), together with the rest of vender's Windows integration.
func systemProxySupported() bool { return false }

func setSystemProxy(on bool, listen string) error {
	// Unreachable: the caller checks systemProxySupported first. It exists so
	// the command compiles identically on both platforms.
	return errors.New("system proxy control is not implemented on windows in this milestone (M4)")
}
