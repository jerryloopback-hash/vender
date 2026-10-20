//go:build windows

package autostart

import (
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func configureNativeWindowsBackend(b *windowsBackend) {
	b.run = runNativeWindowsCommand
	b.lookPath = windowsTool
	b.isElevated = windows.GetCurrentProcessToken().IsElevated
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		b.identityErr = fmt.Errorf("resolve task user SID: %w", err)
	} else {
		b.userSID = user.User.Sid.String()
	}
	b.deleteRunKey = deleteWindowsRunKey
	b.deleteTask = deleteWindowsTask
	b.taskExists = windowsTaskExists
	b.taskExe, b.launcherErr = siblingGUICompanion(b.exe)
}

func siblingGUICompanion(cli string) (string, error) {
	path := filepath.Join(filepath.Dir(cli), "vender-daemon.exe")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect GUI companion: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("autostart companion must be a regular executable file")
	}
	f, err := pe.Open(path)
	if err != nil {
		return "", fmt.Errorf("invalid GUI companion: %w", err)
	}
	defer f.Close()
	var subsystem uint16
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		subsystem = h.Subsystem
	case *pe.OptionalHeader32:
		subsystem = h.Subsystem
	}
	if subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		return "", errors.New("vender-daemon.exe must be built with -ldflags=-H=windowsgui")
	}
	return path, nil
}

func windowsTool(name string) (string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	var path string
	switch name {
	case "reg", "schtasks":
		path = filepath.Join(system, name+".exe")
	case "powershell":
		path = filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	default:
		return "", fmt.Errorf("unsupported Windows startup tool %q", name)
	}
	_, err = os.Stat(path)
	return path, err
}

func runNativeWindowsCommand(name string, args ...string) (string, error) {
	path, err := windowsTool(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	return string(out), err
}

func deleteWindowsRunKey() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, winRunKeyPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	err = key.DeleteValue(winRunValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// COM errors carry HRESULTs independently of the display language. Do not
// mistake an access-denied failure for a missing task on Chinese Windows.
func deleteWindowsTask() error {
	script := `$ErrorActionPreference='Stop'; try { $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $f=$s.GetFolder('\vender'); $f.DeleteTask('daemon',0); exit 0 } catch { $e=$_.Exception; while($null -ne $e.InnerException){$e=$e.InnerException}; if($e.HResult -eq -2147024894 -or $e.HResult -eq -2147024893){exit 0}; [Console]::Error.WriteLine($e.Message); exit 1 }`
	out, err := runNativeWindowsCommand("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return fmt.Errorf("delete Task Scheduler entry: %w%s", err, winOutputSuffix(out))
	}
	return nil
}

func windowsTaskExists() (bool, error) {
	script := `$ErrorActionPreference='Stop'; try { $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $null=$s.GetFolder('\vender').GetTask('daemon'); exit 0 } catch { $e=$_.Exception; while($null -ne $e.InnerException){$e=$e.InnerException}; if($e.HResult -eq -2147024894 -or $e.HResult -eq -2147024893){exit 2}; [Console]::Error.WriteLine($e.Message); exit 1 }`
	out, err := runNativeWindowsCommand("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return false, nil
	}
	return false, fmt.Errorf("inspect Task Scheduler entry: %w%s", err, winOutputSuffix(out))
}
