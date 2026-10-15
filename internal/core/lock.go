package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrLocked reports that another daemon already holds the run directory's
// lock. It is a sentinel: `vender daemon` uses it to refuse a second start and
// `vender status` uses it to decide that a daemon is running.
var ErrLocked = errors.New("core: another vender daemon already holds this run directory")

// Lock is an exclusive, advisory lock on a file. It is held by the daemon for
// its whole lifetime and released by the OS if the process dies, so a crashed
// daemon never leaves a stale lock behind.
type Lock struct {
	file *os.File
	path string
}

// AcquireLock takes the lock at path, creating the file (and its directory) if
// needed. It fails immediately with ErrLocked rather than waiting.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("core: create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("core: open lock file %s: %w", path, err)
	}
	if err := tryLock(f); err != nil {
		f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("core: lock %s: %w", path, err)
	}
	// Record the holder for humans; harmless if it races.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	return &Lock{file: f, path: path}, nil
}

// Release drops the lock. It is idempotent.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlock(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("core: unlock %s: %w", l.path, unlockErr)
	}
	return closeErr
}

// Path returns the lock file's path.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
