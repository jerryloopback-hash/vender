package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Windows system-proxy control, the part that is independent of the Windows
// API. It is deliberately not behind the windows build tag so the registry
// operations can be driven by a fake and the whole flow unit-tested anywhere;
// proxy_windows.go only supplies the real registry and the WinINET refresh.
//
// The proxy WinINET, and so Edge/Chrome, WinHTTP-based tools and anything else
// that reads the user's Internet settings, live in one key:
//
//	HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings
//	    ProxyEnable   REG_DWORD  0 | 1
//	    ProxyServer   REG_SZ     http=127.0.0.1:7897;https=...;socks=...
//	    ProxyOverride REG_SZ     the bypass list
//
// WinHTTP has its own configuration (`netsh winhttp import proxy source=ie`),
// which a browser does not use, so vender does not touch it.
const (
	winInternetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	winProxyEnable      = "ProxyEnable"
	winProxyServer      = "ProxyServer"
	winProxyOverride    = "ProxyOverride"
)

// winProxyBypass keeps loopback traffic away from the proxy: pointing a process
// at vender in order to reach vender would be a loop. "[::1]" and "<local>"
// cover IPv6 and bare NetBIOS-style names.
const winProxyBypass = "localhost;127.*;[::1];<local>"

// winRegistry is the slice of the registry the proxy needs. The real
// implementation (proxy_windows.go) is a thin wrapper over advapi32.
type winRegistry interface {
	// SetString writes name = value under HKEY_CURRENT_USER\subkey as REG_SZ.
	SetString(subkey, name, value string) error
	// SetDWORD writes name = value under HKEY_CURRENT_USER\subkey as REG_DWORD.
	SetDWORD(subkey, name string, value uint32) error
}

// winProxyRefresh notifies WinINET that the settings changed. Writing the
// registry alone is not enough: the proxy is cached, partly per process.
type winProxyRefresh func() error

// applyWindowsProxy points the per-user proxy at listen, or turns it off.
//
// The endpoints are written before ProxyEnable flips, so a failure part way
// through leaves the proxy disabled rather than half-configured — the same
// ordering the GNOME implementation uses. Turning it off only clears
// ProxyEnable; the endpoints stay behind the switch, so nothing the user had
// configured in ProxyServer is destroyed.
func applyWindowsProxy(reg winRegistry, refresh winProxyRefresh, on bool, listen string) error {
	if !on {
		if err := reg.SetDWORD(winInternetSettings, winProxyEnable, 0); err != nil {
			return fmt.Errorf("clear %s: %w", winProxyEnable, err)
		}
		return refreshProxy(refresh)
	}

	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", listen, err)
	}
	if host == "" {
		return fmt.Errorf("listen address %q has no host", listen)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("listen address %q has a non-numeric port", listen)
	}

	if err := reg.SetString(winInternetSettings, winProxyServer, winProxyServerValue(host, port)); err != nil {
		return fmt.Errorf("set %s: %w", winProxyServer, err)
	}
	if err := reg.SetString(winInternetSettings, winProxyOverride, winProxyBypass); err != nil {
		return fmt.Errorf("set %s: %w", winProxyOverride, err)
	}
	// The switch flips last: until it does, the (now correct) endpoints are
	// inert.
	if err := reg.SetDWORD(winInternetSettings, winProxyEnable, 1); err != nil {
		return fmt.Errorf("enable %s: %w", winProxyEnable, err)
	}
	return refreshProxy(refresh)
}

func refreshProxy(refresh winProxyRefresh) error {
	if refresh == nil {
		return nil
	}
	if err := refresh(); err != nil {
		return fmt.Errorf("notify WinINET of the change: %w", err)
	}
	return nil
}

// winProxyServerValue renders WinINET's ProxyServer string: a semicolon
// separated list of [protocol=]host:port entries, one per protocol. Spelling
// all three out (rather than a bare host:port for HTTP only) makes the SOCKS5
// and HTTPS paths behave the way the local listener does.
func winProxyServerValue(host, port string) string {
	hp := net.JoinHostPort(host, port)
	parts := []string{"http=" + hp, "https=" + hp, "socks=" + hp}
	return strings.Join(parts, ";")
}
