//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestValidateArgs(t *testing.T) {
	for _, a := range [][]string{{"daemon"}, {"--launcher-info"}, {"--probe"}, {"daemon", "--config-dir", `C:\用户 甲\配置`, "--run-dir", `C:\用户 甲\缓存`}} {
		if err := validateArgs(a); err != nil {
			t.Fatalf("valid arguments: %v", err)
		}
	}
	for _, a := range [][]string{nil, {"tui"}, {"--probe", "extra"}, {"daemon", "--config-dir"}, {"daemon", "--run-dir", "relative"}, {"daemon", "--unknown", `C:\x`}, {"daemon", "--run-dir", `C:\x`, "--run-dir", `C:\y`}} {
		if validateArgs(a) == nil {
			t.Fatal("accepted unsupported arguments")
		}
	}
}

func TestBoundedTail(t *testing.T) {
	w := &tailWriter{}
	p := bytes.Repeat([]byte("x"), tailLimit*8)
	if n, err := w.Write(p); err != nil || n != len(p) || len(w.Bytes()) != tailLimit {
		t.Fatal("large write not bounded")
	}
	w.Write([]byte("last"))
	if !bytes.HasSuffix(w.Bytes(), []byte("last")) || len(w.Bytes()) != tailLimit {
		t.Fatal("tail did not retain latest output")
	}
}

type consoleReceipt struct {
	Console          uintptr
	Visible          bool
	ChildSameConsole bool
	ChildVisible     bool
}

func TestMain(m *testing.M) {
	if os.Getenv("VENDER_GUI_TEST_FIXTURE") == "1" {
		if filepath.Base(os.Args[0]) == "vender.exe" {
			console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
			visible, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("IsWindowVisible").Call(console)
			r := consoleReceipt{Console: console, Visible: visible != 0}
			if len(os.Args) > 1 && os.Args[1] == "--grandchild" {
				json.NewEncoder(os.Stdout).Encode(r)
				os.Exit(0)
			}
			// Mirrors core.ensureDaemonJob, including nested Task Scheduler jobs.
			if bindLifetime() != nil {
				os.Exit(11)
			}
			cmd := exec.Command(os.Args[0], "--grandchild")
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200}
			b, err := cmd.Output()
			var leaf consoleReceipt
			if err != nil || json.Unmarshal(b, &leaf) != nil {
				os.Exit(12)
			}
			r.ChildSameConsole, r.ChildVisible = console == leaf.Console, leaf.Visible
			b, _ = json.Marshal(r)
			if os.WriteFile(os.Getenv("VENDER_GUI_TEST_RECEIPT"), b, 0o600) != nil {
				os.Exit(13)
			}
			if console == 0 || r.Visible || !r.ChildSameConsole || r.ChildVisible {
				os.Exit(14)
			}
			os.Exit(0)
		}
		code, _ := run(os.Args[1:])
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestHiddenConsoleIncludesGrandchild(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vender.exe", "vender-daemon.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	receipt := filepath.Join(dir, "receipt.json")
	cmd := exec.Command(filepath.Join(dir, "vender-daemon.exe"), "--probe")
	cmd.Env = append(os.Environ(), "VENDER_GUI_TEST_FIXTURE=1", "VENDER_GUI_TEST_RECEIPT="+receipt, "LOCALAPPDATA="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fixture: %v", err)
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("fixture timeout")
	}
	b, err = os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var r consoleReceipt
	if json.Unmarshal(b, &r) != nil || r.Console == 0 || r.Visible || !r.ChildSameConsole || r.ChildVisible {
		t.Fatalf("console inheritance failed: %s", b)
	}
}
