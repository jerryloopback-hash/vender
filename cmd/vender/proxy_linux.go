//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// systemProxySupported reports whether `vender proxy on|off` can change the
// desktop's proxy settings on this platform.
func systemProxySupported() bool { return true }

func forceRestoreSystemProxy() error {
	return errors.New("forced saved-proxy restoration is only supported on Windows")
}

// GNOME's proxy settings are the de-facto standard on Linux desktops: KDE and
// the *BSD/GNOME-alike desktops either share the schema or leave it unused, and
// gsettings writes the same values as the Settings UI, so the change is visible
// and reversible there.
const (
	gnomeProxySchema = "org.gnome.system.proxy"

	// ignoreHosts keeps loopback traffic away from the proxy: pointing a
	// process at vender to reach vender would be a loop.
	gnomeIgnoreHosts = "['localhost', '127.0.0.0/8', '::1']"
)

// setSystemProxy points the GNOME system proxy at listen, or restores it
// (mode: none). The endpoints are written before the mode, so a failure part
// way through leaves the proxy disabled rather than half-configured.
func setSystemProxy(on bool, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", listen, err)
	}
	if _, err := exec.LookPath("gsettings"); err != nil {
		return errors.New("gsettings is not installed; " +
			"vender can only set the system proxy on a GNOME desktop — " +
			"export http_proxy/https_proxy instead (`vender proxy on` prints them)")
	}
	if _, err := gsettings("get", gnomeProxySchema, "mode"); err != nil {
		return fmt.Errorf("the GNOME proxy schema (%s) is not available: %w — "+
			"export http_proxy/https_proxy instead", gnomeProxySchema, err)
	}

	if !on {
		return gsettingsSet(gnomeProxySchema, "mode", "'none'")
	}

	set := func(schema, key, value string) error {
		if err := gsettingsSet(schema, key, value); err != nil {
			return fmt.Errorf("set %s.%s: %w", schema, key, err)
		}
		return nil
	}
	if err := set(gnomeProxySchema, "ignore-hosts", gnomeIgnoreHosts); err != nil {
		return err
	}
	for _, schema := range []string{
		"org.gnome.system.proxy.http",
		"org.gnome.system.proxy.https",
		"org.gnome.system.proxy.socks",
	} {
		if err := set(schema, "host", gvariantQuote(host)); err != nil {
			return err
		}
		if err := set(schema, "port", port); err != nil {
			return err
		}
	}
	// The mode flips last: until it does, the (now correct) endpoints are
	// inert.
	return set(gnomeProxySchema, "mode", "'manual'")
}

// gvariantQuote renders s as a GVariant string literal. gsettings parses its
// value argument as a GVariant, which requires the quotes to be part of the
// argument itself.
func gvariantQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func gsettingsSet(schema, key, value string) error {
	_, err := gsettings("set", schema, key, value)
	return err
}

func gsettings(args ...string) (string, error) {
	out, err := exec.Command("gsettings", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", err
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(string(out)), nil
}
