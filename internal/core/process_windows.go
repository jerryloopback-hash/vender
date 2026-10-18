//go:build windows

package core

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNewProcessGroup detaches the child from vender's console control
// group, so a Ctrl-C in vender's console is not delivered to sing-box twice.
const createNewProcessGroup = 0x00000200

var daemonJob struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

// Enroll the daemon itself BEFORE creating the child. Children then inherit
// membership atomically: there is no Start -> AssignProcess race in which a
// daemon crash can orphan a running, privileged engine. The sole job handle is
// deliberately kept for the daemon's lifetime, never inherited by children.
// Windows 11 supports nested jobs, including under Task Scheduler.
func ensureDaemonJob() error {
	daemonJob.once.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			daemonJob.err = err
			return
		}
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
			windows.CloseHandle(h)
			daemonJob.err = err
			return
		}
		process, err := windows.GetCurrentProcess()
		if err == nil {
			err = windows.AssignProcessToJobObject(h, process)
		}
		if err != nil {
			windows.CloseHandle(h)
			daemonJob.err = err
			return
		}
		daemonJob.handle = h
	})
	return daemonJob.err
}

func prepareProcess(cmd *exec.Cmd) error {
	if err := ensureDaemonJob(); err != nil {
		return fmt.Errorf("cannot establish Windows engine lifetime Job Object: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	return nil
}

// Give Go's interrupt handler a chance to clean up drivers/routes. In a task
// without a shared console this can fail; Stop then uses the kill fallback.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
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
