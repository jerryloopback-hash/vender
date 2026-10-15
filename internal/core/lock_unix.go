//go:build unix

package core

import (
	"os"
	"syscall"
)

// errWouldBlock is the platform-independent "the lock is held elsewhere"
// signal that lock.go maps onto ErrLocked.
var errWouldBlock = syscall.EWOULDBLOCK

// tryLock takes an exclusive, non-blocking flock on f. flock is associated
// with the open file description, so it is released automatically when the
// process exits for any reason — including SIGKILL — which is exactly the
// lifetime a daemon lock wants.
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
