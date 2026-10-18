//go:build windows

package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Runs only on Windows and launches test helpers, never a real engine/TUN.
func TestWindowsJobKillsChildWhenParentExits(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsJobHelper$")
	cmd.Env = append(os.Environ(), "VENDER_JOB_HELPER=parent", "VENDER_JOB_PIDFILE="+pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	// Keep a live handle before allowing the parent to exit. This avoids
	// probing (or terminating) an unrelated process if a PID gets recycled.
	if err := os.WriteFile(pidFile+".release", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper parent: %v", err)
	}
	status, err := windows.WaitForSingleObject(h, 5000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(h, 1)
		t.Fatalf("orphan child survived parent exit: wait=%d err=%v", status, err)
	}
}

func TestWindowsJobHelper(t *testing.T) {
	mode := os.Getenv("VENDER_JOB_HELPER")
	if mode == "" {
		return
	}
	path := os.Getenv("VENDER_JOB_PIDFILE")
	if mode == "child" {
		if err := os.WriteFile(path+".stage", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		if err := os.Rename(path+".stage", path); err != nil {
			os.Exit(3)
		}
		time.Sleep(time.Minute)
		os.Exit(4)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsJobHelper$")
	cmd.Env = append(os.Environ(), "VENDER_JOB_HELPER=child")
	if err := prepareProcess(cmd); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path + ".release"); err == nil {
			os.Exit(0)
		} // deliberately no cleanup
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(5)
}
