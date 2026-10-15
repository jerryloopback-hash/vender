//go:build unix

package core

import (
	"os/exec"
	"syscall"
)

// prepareProcess puts the child into its own process group, so that vender can
// signal the whole tree (sing-box spawns helper goroutines but no children
// today; a group kill still guarantees nothing is orphaned) without touching
// its own process group.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess asks the child to exit gracefully, and its whole process
// group with it.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killProcess is the escalation path: SIGKILL cannot be ignored.
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
