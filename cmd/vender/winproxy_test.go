package main

import (
	"errors"
	"strings"
	"testing"
)

type fakeProxyAPI struct {
	settings    winProxySettings
	writes      int
	failWrite   bool
	failRefresh bool
}

func (a *fakeProxyAPI) Read() (winProxySettings, error) { return a.settings, nil }
func (a *fakeProxyAPI) Write(s winProxySettings) error {
	a.writes++
	a.settings = s
	if a.failWrite {
		a.failWrite = false
		return errors.New("partial write failure")
	}
	return nil
}
func (a *fakeProxyAPI) Refresh() error {
	if a.failRefresh {
		a.failRefresh = false
		return errors.New("refresh failed")
	}
	return nil
}

type fakeProxyStore struct {
	backup   *winProxyBackup
	failSave bool
}

func (s *fakeProxyStore) Load() (*winProxyBackup, error) { return s.backup, nil }
func (s *fakeProxyStore) Save(b winProxyBackup) error {
	if s.failSave {
		return errors.New("backup failed")
	}
	s.backup = &b
	return nil
}
func (s *fakeProxyStore) Clear() error { s.backup = nil; return nil }

func TestWindowsProxyRestoresPACAndOriginalAcrossRepeatedOn(t *testing.T) {
	original := winProxySettings{Flags: 1 | 4 | 8, Server: "old:80", Bypass: "*.intranet", AutoConfigURL: "https://example.invalid/proxy.pac"}
	a, s := &fakeProxyAPI{settings: original}, &fakeProxyStore{}
	for _, listen := range []string{"127.0.0.1:7897", "[::1]:7898"} {
		if err := applyWindowsProxy(a, s, true, listen, false); err != nil {
			t.Fatal(err)
		}
		if a.settings.Flags != 3 || a.settings.AutoConfigURL != "" {
			t.Fatal("PAC/autodetect still active")
		}
	}
	if err := applyWindowsProxy(a, s, false, "", false); err != nil {
		t.Fatal(err)
	}
	if a.settings != original || s.backup != nil {
		t.Fatal("original settings not restored")
	}
	writes := a.writes
	if err := applyWindowsProxy(a, s, false, "", false); err != nil || a.writes != writes {
		t.Fatal("repeated off modified another proxy")
	}
}

func TestWindowsProxyBackupMustSucceedBeforeAnyWrite(t *testing.T) {
	a, s := &fakeProxyAPI{}, &fakeProxyStore{failSave: true}
	if err := applyWindowsProxy(a, s, true, "127.0.0.1:7897", false); err == nil || a.writes != 0 {
		t.Fatal("changed proxy without durable backup")
	}
}

func TestWindowsProxyFailureRollsBack(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		original := winProxySettings{Flags: 8, AutoConfigURL: "https://example.invalid/a"}
		a, s := &fakeProxyAPI{settings: original, failWrite: !refresh, failRefresh: refresh}, &fakeProxyStore{}
		if err := applyWindowsProxy(a, s, true, "127.0.0.1:7897", false); err == nil {
			t.Fatal("failure swallowed")
		}
		if a.settings != original || s.backup != nil {
			t.Fatal("rollback failed")
		}
	}
}

func TestWindowsProxyConflictsRequireExplicitForce(t *testing.T) {
	a, s := &fakeProxyAPI{settings: winProxySettings{Flags: 8}}, &fakeProxyStore{}
	original := a.settings
	if err := applyWindowsProxy(a, s, true, "127.0.0.1:7897", false); err != nil {
		t.Fatal(err)
	}
	a.settings = winProxySettings{Flags: 3, Server: "another:123"}
	if err := applyWindowsProxy(a, s, false, "", false); err == nil {
		t.Fatal("external settings silently overwritten")
	}
	if a.settings.Server != "another:123" || s.backup == nil {
		t.Fatal("conflict lost data")
	}
	if err := applyWindowsProxy(a, s, false, "", true); err != nil || a.settings != original {
		t.Fatalf("explicit recovery failed: %v", err)
	}
}

func TestWindowsProxyCanRecoverInterruptedApply(t *testing.T) {
	original := winProxySettings{Flags: 8}
	s := &fakeProxyStore{backup: &winProxyBackup{Version: 1, Original: original, Applied: winProxySettings{Flags: 3, Server: "planned"}}}
	a := &fakeProxyAPI{settings: original}
	if err := applyWindowsProxy(a, s, false, "", false); err != nil || s.backup != nil {
		t.Fatal("interrupted write not recoverable")
	}
}

func TestWindowsProxyRejectsBadAddressBeforeMutation(t *testing.T) {
	for _, listen := range []string{"bad", "127.0.0.1:0", "127.0.0.1:65536"} {
		a, s := &fakeProxyAPI{}, &fakeProxyStore{}
		if err := applyWindowsProxy(a, s, true, listen, false); err == nil || a.writes != 0 || s.backup != nil {
			t.Fatalf("accepted %s", listen)
		}
	}
}

func TestWinProxyServerValueBracketsIPv6(t *testing.T) {
	if !strings.Contains(winProxyServerValue("::1", "7897"), "http=[::1]:7897") {
		t.Fatal("missing IPv6 brackets")
	}
}
