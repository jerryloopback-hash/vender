//go:build windows

// Command vender-daemon is the GUI-subsystem logon companion for vender.exe.
// Build with -ldflags=-H=windowsgui. Keep vender.exe a console application.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ll-lite/vender/internal/privatefs"
	"golang.org/x/sys/windows"
)

var (
	version     = "dev"
	commit      = "none"
	date        = "unknown"
	lifetimeJob windows.Handle
)

const tailLimit = 32 * 1024

type tailWriter struct {
	mu   sync.Mutex
	data []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if n >= tailLimit {
		w.data = append(w.data[:0], p[n-tailLimit:]...)
	} else {
		w.data = append(w.data, p...)
		if len(w.data) > tailLimit {
			w.data = bytes.Clone(w.data[len(w.data)-tailLimit:])
		}
	}
	return n, nil
}

func (w *tailWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data)
}

func validateArgs(args []string) error {
	if len(args) == 1 && (args[0] == "--launcher-info" || args[0] == "--probe") {
		return nil
	}
	if len(args) == 0 || args[0] != "daemon" {
		return errors.New("expected daemon, --launcher-info or --probe")
	}
	seen := map[string]bool{}
	for i := 1; i < len(args); i += 2 {
		if i+1 >= len(args) || (args[i] != "--config-dir" && args[i] != "--run-dir") || seen[args[i]] || !filepath.IsAbs(args[i+1]) {
			return errors.New("daemon accepts only unique --config-dir and --run-dir with absolute paths")
		}
		seen[args[i]] = true
	}
	return nil
}

func logDir(args []string) (string, error) {
	if validateArgs(args) != nil {
		args = nil
	}
	for i := 1; i+1 < len(args); i += 2 {
		if args[i] == "--run-dir" {
			return args[i+1], nil
		}
	}
	dir, err := os.UserCacheDir()
	return filepath.Join(dir, "vender"), err
}

func writeFailure(args []string, data []byte) {
	dir, err := logDir(args)
	if err != nil || privatefs.EnsureDir(dir) != nil {
		return
	}
	path := filepath.Join(dir, "windowless-launcher-error.log")
	if privatefs.Protect(path) != nil {
		return
	}
	if os.WriteFile(path, data, 0o600) == nil {
		_ = privatefs.Protect(path)
	}
}

func bindLifetime() error {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(h)
		return err
	}
	p, err := windows.GetCurrentProcess()
	if err == nil {
		err = windows.AssignProcessToJobObject(h, p)
	}
	if err != nil {
		windows.CloseHandle(h)
		return err
	}
	// Retain this handle for the launcher's entire life. Closing it earlier
	// would terminate this process too. Windows closes it on process exit.
	lifetimeJob = h
	return nil
}

func run(args []string) (int, error) {
	if err := validateArgs(args); err != nil {
		return 2, err
	}
	if args[0] == "--launcher-info" {
		return 0, json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol": 1, "version": version, "commit": commit, "date": date, "peer": "vender.exe"})
	}
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	bin := filepath.Dir(exe)
	peer := filepath.Join(bin, "vender.exe")
	if _, err := os.Stat(peer); err != nil {
		return 1, fmt.Errorf("matching sibling vender.exe is required: %w", err)
	}
	if err := bindLifetime(); err != nil {
		return 1, fmt.Errorf("bind process-tree lifetime: %w", err)
	}
	probe := args[0] == "--probe"
	if probe {
		args = []string{"version"}
	}
	tail := &tailWriter{}
	cmd := exec.Command(peer, args...)
	cmd.Dir = bin
	cmd.Stdout, cmd.Stderr = tail, tail
	// A hidden console, rather than CREATE_NO_WINDOW, matters: sing-box creates
	// a new process group and must inherit this hidden console, not allocate a
	// new visible console because its parent has none.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	err = cmd.Wait()
	if err != nil {
		writeFailure(args, append([]byte(err.Error()+"\r\n"), tail.Bytes()...))
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

func main() {
	args := os.Args[1:]
	code, err := run(args)
	if err != nil {
		writeFailure(args, []byte(err.Error()))
	}
	os.Exit(code)
}
