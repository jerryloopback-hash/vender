package autostart

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// Windows registration. This file is deliberately *not* behind the windows
// build tag: everything here is pure string/argument construction plus a small
// injectable runner, so the whole mechanism is unit-tested on any platform,
// while the thin `autostart_windows.go` only wires it to the real executables.
//
// The preferred mechanism is a Task Scheduler logon task, because it can be
// registered **hidden** and with the least privilege, and because a scheduled
// task (unlike a Run key value) survives a per-application "startup apps"
// disable toggle. The per-user Registry Run key is the fallback when the task
// cannot be created — notably on a non-elevated account, where writing to the
// root task folder can be refused.
const (
	// winTaskName is the Task Scheduler path (`\vender\daemon`); the XML's
	// <URI> must match it.
	winTaskName = `\vender\daemon`
	// winRunKeyPath is the per-user Run key, relative to HKEY_CURRENT_USER.
	winRunKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	// winRunValueName is the value name used inside the Run key.
	winRunValueName = "vender"
)

// winRunner runs an external command and returns its combined output. It is a
// seam so tests can drive schtasks/reg without Windows.
type winRunner func(name string, args ...string) (string, error)

// windowsBackend is every ambient input the Windows implementation reads. It is
// a value with injectable fields.
type windowsBackend struct {
	exe      string
	run      winRunner
	lookPath func(string) (string, error)
	// writeTemp writes the task XML to a temporary file and returns its path.
	writeTemp func(content string) (string, error)
}

func defaultWindowsBackend() windowsBackend {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	} else if abs, aerr := filepath.Abs(exe); aerr == nil {
		exe = abs
	}
	return windowsBackend{
		exe:       exe,
		run:       runWindowsCommand,
		lookPath:  exec.LookPath,
		writeTemp: writeTaskXMLTemp,
	}
}

func runWindowsCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func writeTaskXMLTemp(content string) (string, error) {
	f, err := os.CreateTemp("", "vender-task-*.xml")
	if err != nil {
		return "", fmt.Errorf("autostart: create the task XML: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(utf16leBytes(content)); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("autostart: write the task XML: %w", err)
	}
	return f.Name(), nil
}

// --- enable -----------------------------------------------------------------

func (b windowsBackend) enable() (State, error) {
	if b.exe == "" {
		return State{}, errors.New("autostart: cannot determine the vender executable path (os.Executable failed)")
	}

	var taskErr error
	if _, err := b.lookPath("schtasks"); err == nil {
		st, err := b.enableTask()
		if err == nil {
			return st, nil
		}
		taskErr = err
	} else {
		taskErr = errors.New("schtasks is not available")
	}

	st, err := b.enableRunKey()
	if err != nil {
		return State{}, fmt.Errorf("autostart: the Task Scheduler task could not be created (%v) and the Run key fallback failed: %w", taskErr, err)
	}
	st.Detail += "; the Task Scheduler task could not be created (" + taskErr.Error() + "), so the per-user Run key is used instead"
	return st, nil
}

func (b windowsBackend) enableTask() (State, error) {
	if b.writeTemp == nil {
		return State{}, errors.New("autostart: no way to stage the task XML")
	}
	xmlPath, err := b.writeTemp(taskXML(b.exe))
	if err != nil {
		return State{}, err
	}
	defer os.Remove(xmlPath)

	out, err := b.run("schtasks", "/Create", "/TN", winTaskName, "/XML", xmlPath, "/F")
	if err != nil {
		return State{}, fmt.Errorf("schtasks /Create /TN %s: %w%s", winTaskName, err, winOutputSuffix(out))
	}
	return State{
		Enabled:   true,
		Mechanism: MechanismWindowsTask,
		Target:    winTaskName,
		Detail: "Task Scheduler logon task created (least privilege, hidden); " +
			"runs `vender daemon` at logon. A short console window can still appear because vender is a console-subsystem binary",
	}, nil
}

func (b windowsBackend) enableRunKey() (State, error) {
	if _, err := b.lookPath("reg"); err != nil {
		return State{}, errors.New("reg.exe is not available")
	}
	value := runKeyValue(b.exe)
	out, err := b.run("reg", "add", winRunKeyPath, "/v", winRunValueName, "/t", "REG_SZ", "/d", value, "/f")
	if err != nil {
		return State{}, fmt.Errorf("reg add %s\\%s: %w%s", winRunKeyPath, winRunValueName, err, winOutputSuffix(out))
	}
	return State{
		Enabled:   true,
		Mechanism: MechanismWindowsRunKey,
		Target:    `HKCU\` + winRunKeyPath + `\` + winRunValueName,
		Detail: "per-user Registry Run key set to " + value +
			"; it starts the daemon at logon. A console window appears briefly because vender is a console-subsystem binary",
	}, nil
}

// --- disable ----------------------------------------------------------------

// disable removes both artifacts, so a machine that switched mechanisms between
// runs does not end up with two entries. Removing what is not there is not an
// error.
func (b windowsBackend) disable() error {
	var firstErr error

	if _, err := b.lookPath("schtasks"); err == nil {
		if out, err := b.run("schtasks", "/Delete", "/TN", winTaskName, "/F"); err != nil && !winNotFound(out) {
			if firstErr == nil {
				firstErr = fmt.Errorf("schtasks /Delete /TN %s: %w%s", winTaskName, err, winOutputSuffix(out))
			}
		}
	}
	if _, err := b.lookPath("reg"); err == nil {
		if out, err := b.run("reg", "delete", winRunKeyPath, "/v", winRunValueName, "/f"); err != nil && !winNotFound(out) {
			if firstErr == nil {
				firstErr = fmt.Errorf("reg delete %s\\%s: %w%s", winRunKeyPath, winRunValueName, err, winOutputSuffix(out))
			}
		}
	}
	return firstErr
}

// --- status -----------------------------------------------------------------

func (b windowsBackend) status() (State, error) {
	if _, err := b.lookPath("schtasks"); err == nil {
		if out, err := b.run("schtasks", "/Query", "/TN", winTaskName, "/XML"); err == nil {
			if looksLikeTaskXML(out) {
				return State{
					Enabled:   true,
					Mechanism: MechanismWindowsTask,
					Target:    winTaskName,
					Detail:    "Task Scheduler logon task " + winTaskName + " is registered" + describeTaskCommand(out),
				}, nil
			}
		}
	}

	if _, err := b.lookPath("reg"); err == nil {
		if value, ok := b.runKeyValue(); ok {
			return State{
				Enabled:   true,
				Mechanism: MechanismWindowsRunKey,
				Target:    `HKCU\` + winRunKeyPath + `\` + winRunValueName,
				Detail:    "per-user Registry Run key = " + value,
			}, nil
		}
	}

	// Nothing installed: say what `on` would do.
	if _, err := b.lookPath("schtasks"); err == nil {
		return State{
			Enabled:   false,
			Mechanism: MechanismWindowsTask,
			Target:    winTaskName,
			Detail:    "not registered; `vender autostart on` would create a Task Scheduler logon task (falling back to the per-user Run key if that is refused)",
		}, nil
	}
	return State{
		Enabled:   false,
		Mechanism: MechanismWindowsRunKey,
		Target:    `HKCU\` + winRunKeyPath + `\` + winRunValueName,
		Detail:    "not registered; `vender autostart on` would set the per-user Registry Run key",
	}, nil
}

func (b windowsBackend) runKeyValue() (string, bool) {
	out, err := b.run("reg", "query", winRunKeyPath, "/v", winRunValueName)
	if err != nil {
		return "", false
	}
	return parseRegQuery(out)
}

// --- pure helpers (unit-tested on any platform) ------------------------------

// runKeyValue is the command line stored in the Run key. Quoting the path
// matters when it contains spaces: without it Windows would treat the tail of
// the path as the argument.
func runKeyValue(exe string) string {
	return `"` + exe + `" daemon`
}

// taskXML is the Task Scheduler definition registered with `schtasks /Create
// /XML`. It is a logon trigger that runs in the interactive user's session, at
// the least privilege, as a hidden task.
//
// Note on the console window: `<Hidden>true</Hidden>` removes the task from the
// Task Scheduler library view and suppresses the default window in the common
// case, but a console-subsystem executable can still flash a console. The
// guaranteed windowless variant is a GUI-subsystem build or a `conhost
// --headless` launcher, both deferred to M6; the daemon itself needs no
// console to work.
func taskXML(exe string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>` + "\n" +
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n" +
		`  <RegistrationInfo>` + "\n" +
		`    <Description>vender proxy client: start the daemon at logon</Description>` + "\n" +
		`    <URI>` + xmlEscape(winTaskName) + `</URI>` + "\n" +
		`  </RegistrationInfo>` + "\n" +
		`  <Triggers>` + "\n" +
		`    <LogonTrigger>` + "\n" +
		`      <Enabled>true</Enabled>` + "\n" +
		`    </LogonTrigger>` + "\n" +
		`  </Triggers>` + "\n" +
		`  <Principals>` + "\n" +
		`    <Principal id="Author">` + "\n" +
		`      <LogonType>InteractiveToken</LogonType>` + "\n" +
		`      <RunLevel>LeastPrivilege</RunLevel>` + "\n" +
		`    </Principal>` + "\n" +
		`  </Principals>` + "\n" +
		`  <Settings>` + "\n" +
		`    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>` + "\n" +
		`    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>` + "\n" +
		`    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>` + "\n" +
		`    <AllowHardTerminate>true</AllowHardTerminate>` + "\n" +
		`    <StartWhenAvailable>false</StartWhenAvailable>` + "\n" +
		`    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>` + "\n" +
		`    <IdleSettings>` + "\n" +
		`      <StopOnIdleEnd>false</StopOnIdleEnd>` + "\n" +
		`      <RestartOnIdle>false</RestartOnIdle>` + "\n" +
		`    </IdleSettings>` + "\n" +
		`    <AllowStartOnDemand>true</AllowStartOnDemand>` + "\n" +
		`    <Enabled>true</Enabled>` + "\n" +
		`    <Hidden>true</Hidden>` + "\n" +
		`    <RunOnlyIfIdle>false</RunOnlyIfIdle>` + "\n" +
		`    <WakeToRun>false</WakeToRun>` + "\n" +
		`    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>` + "\n" +
		`    <Priority>7</Priority>` + "\n" +
		`  </Settings>` + "\n" +
		`  <Actions Context="Author">` + "\n" +
		`    <Exec>` + "\n" +
		`      <Command>` + xmlEscape(exe) + `</Command>` + "\n" +
		`      <Arguments>daemon</Arguments>` + "\n" +
		`    </Exec>` + "\n" +
		`  </Actions>` + "\n" +
		`</Task>` + "\n"
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// utf16leBytes encodes s as UTF-16LE with a byte-order mark, which is what
// `schtasks /Create /XML` requires of its input file.
func utf16leBytes(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+len(units)*2)
	out = append(out, 0xFF, 0xFE) // UTF-16LE BOM
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// looksLikeTaskXML reports whether schtasks /Query /XML produced a task
// definition rather than an error message.
func looksLikeTaskXML(out string) bool {
	return strings.Contains(normalizeWinOutput(out), "<Task")
}

// describeTaskCommand appends the executable the queried task actually runs, so
// `autostart status` can be checked against reality. The XML is UTF-16LE with
// NUL bytes, which is why the output goes through normalizeWinOutput first.
func describeTaskCommand(out string) string {
	s := normalizeWinOutput(out)
	start := strings.Index(s, "<Command>")
	end := strings.Index(s, "</Command>")
	if start < 0 || end < 0 || end < start {
		return ""
	}
	cmd := strings.TrimSpace(s[start+len("<Command>") : end])
	if cmd == "" {
		return ""
	}
	return "; it runs " + cmd
}

// normalizeWinOutput salvages text from the mixed encodings command-line tools
// emit on Windows. schtasks and reg write UTF-16LE when their output is a pipe,
// so every second byte is NUL; dropping the NULs recovers the ASCII subset both
// tools actually use.
func normalizeWinOutput(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// winNotFound reports whether a reg/schtasks failure is the benign "there is
// nothing to delete" case, which must make `off` idempotent rather than an
// error.
func winNotFound(out string) bool {
	s := strings.ToLower(normalizeWinOutput(out))
	for _, marker := range []string{
		"cannot find the file specified", // missing task
		"unable to find the specified registry key",
		"the system was unable to find the specified registry key",
		"cannot find the registry key",
		"the system cannot find the path specified",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// parseRegQuery extracts the value from `reg query <key> /v <name>` output:
//
//	HKEY_CURRENT_USER\Software\...\Run
//	    vender    REG_SZ    "C:\path\vender.exe" daemon
func parseRegQuery(out string) (string, bool) {
	for _, line := range strings.Split(normalizeWinOutput(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.HasPrefix(fields[1], "REG_") {
			continue
		}
		// The value is everything after the type, including any spaces in the
		// (quoted) command line.
		idx := strings.Index(line, fields[1]) + len(fields[1])
		return strings.TrimSpace(line[idx:]), true
	}
	return "", false
}

func winOutputSuffix(out string) string {
	if msg := firstLine(normalizeWinOutput(out)); msg != "" {
		return ": " + msg
	}
	return ""
}
