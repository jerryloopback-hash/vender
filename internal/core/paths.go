package core

import (
	"fmt"
	"os"
	"path/filepath"
)

// Files that live in the run directory. The run directory is *runtime* state
// (a generated config, a lock, a pid), so it belongs under the user's cache
// directory — never under the config directory, which holds the user's
// hand-written YAML and is meant to be editable and backed up.
const (
	// LockFileName is held (advisory, exclusive) for as long as a daemon runs.
	LockFileName = "vender.lock"
	// PidFileName records the daemon's process id, purely for display in
	// `vender status`; the lock, not the pid, is what actually enforces
	// one-daemon-per-run-dir.
	PidFileName = "vender.pid"
)

// DefaultRunDir returns the platform runtime directory for vender:
//
//	Linux   $XDG_CACHE_HOME/vender   (default ~/.cache/vender)
//	Windows %LocalAppData%\vender
//
// It does not create the directory; WriteConfig and AcquireLock do that.
func DefaultRunDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("core: locate user cache dir: %w", err)
	}
	return filepath.Join(base, "vender"), nil
}

// ConfigPath returns the path of the generated sing-box configuration inside
// runDir.
func ConfigPath(runDir string) string {
	return filepath.Join(runDir, ConfigFileName)
}

// LockPath returns the path of the daemon lock file inside runDir.
func LockPath(runDir string) string {
	return filepath.Join(runDir, LockFileName)
}

// PidPath returns the path of the daemon pid file inside runDir.
func PidPath(runDir string) string {
	return filepath.Join(runDir, PidFileName)
}

// ReadPid returns the process id recorded in runDir's pid file, or 0 when the
// file is missing or unreadable. It is advisory information for `vender
// status`; callers must not use it to decide whether a daemon runs.
func ReadPid(runDir string) int {
	data, err := os.ReadFile(PidPath(runDir))
	if err != nil {
		return 0
	}
	pid := 0
	for _, r := range string(data) {
		if r < '0' || r > '9' {
			break
		}
		pid = pid*10 + int(r-'0')
	}
	return pid
}

// WritePid records the current process id in runDir's pid file.
func WritePid(runDir string) error {
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("core: create run dir %s: %w", runDir, err)
	}
	body := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(PidPath(runDir), []byte(body), 0o600); err != nil {
		return fmt.Errorf("core: write pid file: %w", err)
	}
	return nil
}

// RemovePid deletes runDir's pid file, ignoring a missing file.
func RemovePid(runDir string) {
	_ = os.Remove(PidPath(runDir))
}
