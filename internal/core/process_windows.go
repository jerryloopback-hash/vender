//go:build windows

package core

import (
	"os"
	"os/exec"
	"syscall"
)

// createNewProcessGroup detaches the child from vender's console control
// group, so a Ctrl-C in vender's console is not delivered to sing-box twice.
const createNewProcessGroup = 0x00000200

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// terminateProcess on Windows has no SIGTERM: TerminateProcess is the only
// reliable way to end a detached child. The grace period in Stop still applies
// (a process that exits on its own is noticed first), but there is no
// in-band "please shut down" signal to send.
func terminateProcess(cmd *exec.Cmd) error {
	return killProcess(cmd)
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err != nil && errorsIsProcessDone(err) {
		return nil
	}
	return err
}

func errorsIsProcessDone(err error) bool {
	return err == os.ErrProcessDone
}
