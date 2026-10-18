package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Effective LAN WinINET settings, including PAC and automatic discovery flags.
// WinHTTP and dial-up connection profiles are deliberately not changed.
type winProxySettings struct {
	Flags         uint32 `json:"flags"`
	Server        string `json:"server"`
	Bypass        string `json:"bypass"`
	AutoConfigURL string `json:"autoConfigURL"`
}

const winProxyBypass = "localhost;127.*;[::1];<local>"
const winProxyDirect = 1
const winProxyManual = 2

type winProxyBackup struct {
	Version  int              `json:"version"`
	Original winProxySettings `json:"original"`
	Applied  winProxySettings `json:"applied"`
}

type winProxyAPI interface {
	Read() (winProxySettings, error)
	Write(winProxySettings) error
	Refresh() error
}

// Save must be durable before Write changes any OS settings. The Windows
// implementation uses a single HKCU value, scoped to the user, not run-dir.
type winProxyBackupStore interface {
	Load() (*winProxyBackup, error)
	Save(winProxyBackup) error
	Clear() error
}

// applyWindowsProxy is serialized by a per-user lock in the native wrapper.
// A durable original survives CLI/daemon crashes and repeated `on` invocations.
// Conflicting external edits are never silently overwritten by `off`.
func applyWindowsProxy(api winProxyAPI, store winProxyBackupStore, on bool, listen string, force bool) error {
	var target winProxySettings
	if on {
		host, port, err := net.SplitHostPort(listen)
		if err != nil || host == "" {
			return fmt.Errorf("invalid proxy listen address %q", listen)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("invalid proxy port in %q", listen)
		}
		target = winProxySettings{Flags: winProxyDirect | winProxyManual, Server: winProxyServerValue(host, port), Bypass: winProxyBypass}
	}
	backup, err := store.Load()
	if err != nil {
		return fmt.Errorf("load system proxy backup: %w", err)
	}
	if backup != nil && backup.Version != 1 {
		return errors.New("unsupported system proxy backup version; original settings were not changed")
	}
	if !on && backup == nil {
		// Never disable a proxy configured by somebody else, including when
		// this is a repeated `off` or a fresh vender installation.
		return nil
	}
	current, err := api.Read()
	if err != nil {
		return err
	}
	if backup != nil && current != backup.Applied && current != backup.Original && !force {
		return errors.New("system proxy changed outside vender; backup retained; use `vender proxy off --force` only to deliberately restore the saved original")
	}
	if !on {
		if err := writeAndRefresh(api, backup.Original); err != nil {
			return fmt.Errorf("restore system proxy (backup retained): %w", err)
		}
		return store.Clear()
	}
	original := current
	if backup != nil {
		original = backup.Original
	}
	next := winProxyBackup{Version: 1, Original: original, Applied: target}
	if err := store.Save(next); err != nil {
		return fmt.Errorf("save system proxy backup before applying settings: %w", err)
	}
	if err := writeAndRefresh(api, target); err != nil {
		// Write or refresh can fail after modifying effective settings. Restore
		// the state observed at entry, and retain the original on any failure.
		if restoreErr := writeAndRefresh(api, current); restoreErr != nil {
			return fmt.Errorf("apply failed: %w; rollback failed: %v; backup retained for `proxy off --force`", err, restoreErr)
		}
		var storeErr error
		if backup == nil {
			storeErr = store.Clear()
		} else {
			storeErr = store.Save(*backup)
		}
		return errors.Join(fmt.Errorf("apply system proxy failed; previous settings restored: %w", err), storeErr)
	}
	return nil
}

func writeAndRefresh(api winProxyAPI, settings winProxySettings) error {
	if err := api.Write(settings); err != nil {
		return err
	}
	return api.Refresh()
}

func winProxyServerValue(host, port string) string {
	hp := net.JoinHostPort(host, port)
	return strings.Join([]string{"http=" + hp, "https=" + hp, "socks=" + hp}, ";")
}
