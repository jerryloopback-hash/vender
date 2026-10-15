package core

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	path := LockPath(dir)

	first, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}

	second, err := AcquireLock(path)
	if err == nil {
		second.Release()
		t.Fatal("second AcquireLock = nil error, want ErrLocked")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("second AcquireLock error = %v, want it to wrap ErrLocked", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %q, want it to name %q", err, path)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// After a release the lock is free again.
	third, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after Release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("third Release: %v", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	l, err := AcquireLock(LockPath(t.TempDir()))
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Errorf("second Release: %v, want nil", err)
	}
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Errorf("nil Lock.Release() = %v, want nil", err)
	}
	if got := nilLock.Path(); got != "" {
		t.Errorf("nil Lock.Path() = %q, want empty", got)
	}
}

func TestAcquireLockCreatesMissingDirectories(t *testing.T) {
	path := LockPath(filepath.Join(t.TempDir(), "deep", "nested", "run"))
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer l.Release()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file was not created: %v", err)
	}
}

func TestAcquireLockRecordsTheHolder(t *testing.T) {
	path := LockPath(t.TempDir())
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer l.Release()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(body), strconv.Itoa(os.Getpid())) {
		t.Errorf("lock file = %q, want it to start with this pid (%d)", body, os.Getpid())
	}
}

func TestPidFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := ReadPid(dir); got != 0 {
		t.Errorf("ReadPid on an empty dir = %d, want 0", got)
	}
	if err := WritePid(dir); err != nil {
		t.Fatalf("WritePid: %v", err)
	}
	if got, want := ReadPid(dir), os.Getpid(); got != want {
		t.Errorf("ReadPid = %d, want %d", got, want)
	}
	if _, err := os.Stat(PidPath(dir)); err != nil {
		t.Errorf("pid file missing: %v", err)
	}
	RemovePid(dir)
	if got := ReadPid(dir); got != 0 {
		t.Errorf("ReadPid after RemovePid = %d, want 0", got)
	}
	RemovePid(dir) // removing twice is fine
}

func TestReadPidIgnoresGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(PidPath(dir), []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := ReadPid(dir); got != 0 {
		t.Errorf("ReadPid on garbage = %d, want 0", got)
	}
}

func TestDefaultRunDir(t *testing.T) {
	dir, err := DefaultRunDir()
	if err != nil {
		t.Fatalf("DefaultRunDir: %v", err)
	}
	if !strings.HasSuffix(dir, "vender") {
		t.Errorf("DefaultRunDir() = %q, want it to end in vender", dir)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	if got, want := dir, filepath.Join(base, "vender"); got != want {
		t.Errorf("DefaultRunDir() = %q, want %q", got, want)
	}
}

func TestConfigPath(t *testing.T) {
	if got, want := ConfigPath("/run/vender"), filepath.Join("/run/vender", ConfigFileName); got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
	if ConfigFileName != "singbox.json" {
		t.Errorf("ConfigFileName = %q, want singbox.json (frozen in M0)", ConfigFileName)
	}
}
