package autostart

import (
	"encoding/binary"
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
	writeTemp    func(content string) (string, error)
	userSID      string
	identityErr  error
	deleteTask   func() error
	deleteRunKey func() error
	taskExists   func() (bool, error)
	isElevated   func() bool
	// taskExe is the optional GUI companion; exe remains the original CLI.
	taskExe     string
	launcherErr error
}

func defaultWindowsBackend() windowsBackend {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	} else if abs, aerr := filepath.Abs(exe); aerr == nil {
		exe = abs
	}
	b := windowsBackend{
		exe:       exe,
		run:       runWindowsCommand,
		lookPath:  exec.LookPath,
		writeTemp: writeTaskXMLTemp,
	}
	configureNativeWindowsBackend(&b)
	return b
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

func (b windowsBackend) enable() (State, error) { return b.enableWith(Options{}) }

// enableWith registers the logon task. With TUN mode the task must run elevated
// (a WinTun adapter is a driver-level device), which is a *different* task
// definition — HighestAvailable instead of LeastPrivilege — and which the
// per-user Run key cannot express at all. So for TUN the task is the only
// acceptable mechanism: falling back to the Run key would install something
// that starts a daemon that cannot create the TUN.
func (b windowsBackend) enableWith(o Options) (State, error) {
	if b.identityErr != nil {
		return State{}, b.identityErr
	}
	if b.launcherErr != nil {
		return State{}, b.launcherErr
	}
	if o.TUN && b.isElevated != nil && !b.isElevated() {
		return State{}, errors.New("registering TUN autostart requires an elevated Administrator prompt; HighestAvailable cannot grant Administrator rights to a standard user")
	}
	if b.exe == "" {
		return State{}, errors.New("autostart: cannot determine the vender executable path (os.Executable failed)")
	}

	var taskErr error
	if _, err := b.lookPath("schtasks"); err == nil {
		st, err := b.enableTaskWith(o)
		if err == nil {
			if b.deleteRunKey != nil {
				if err := b.deleteRunKey(); err != nil {
					return st, fmt.Errorf("task registered, but removing old Run entry failed: %w", err)
				}
			}
			return st, nil
		}
		taskErr = err
	} else {
		taskErr = errors.New("schtasks is not available")
	}

	if o.TUN {
		return State{}, fmt.Errorf("autostart: TUN mode needs a logon task registered with the highest privileges, "+
			"and the task could not be created (%v); the per-user Run key cannot elevate, so it is not installed. "+
			"Run `vender autostart on` once from an elevated (Administrator) prompt%s", taskErr, b.capabilityNoteSuffix(o))
	}
	// A refused update of an existing task must not install a second startup
	// mechanism pointing at another configuration or privilege level.
	if b.taskExists != nil {
		exists, err := b.taskExists()
		if err != nil {
			return State{}, fmt.Errorf("task registration failed (%v); cannot safely check existing task: %w", taskErr, err)
		}
		if exists {
			return State{}, fmt.Errorf("task registration failed (%v); existing task retained, Run-key fallback not installed", taskErr)
		}
	}

	st, err := b.enableRunKeyWith(o)
	if err != nil {
		return State{}, fmt.Errorf("autostart: the Task Scheduler task could not be created (%v) and the Run key fallback failed: %w", taskErr, err)
	}
	st.Detail += "; the Task Scheduler task could not be created (" + taskErr.Error() + "), so the per-user Run key is used instead"
	return st, nil
}

// capabilityNoteSuffix quotes the caller's privilege verdict, so the error says
// both what failed and what is still missing.
func (b windowsBackend) capabilityNoteSuffix(o Options) string {
	if o.CapabilityHint == "" {
		return ""
	}
	return "; the current verdict: " + o.CapabilityHint
}

func (b windowsBackend) enableTask(elevated bool) (State, error) {
	return b.enableTaskWith(Options{TUN: elevated})
}

func (b windowsBackend) enableTaskWith(o Options) (State, error) {
	if b.writeTemp == nil {
		return State{}, errors.New("autostart: no way to stage the task XML")
	}
	command := b.exe
	if b.taskExe != "" {
		command = b.taskExe
	}
	xmlPath, err := b.writeTemp(taskXMLWith(command, o, b.userSID))
	if err != nil {
		return State{}, err
	}
	defer os.Remove(xmlPath)

	out, err := b.run("schtasks", "/Create", "/TN", winTaskName, "/XML", xmlPath, "/F")
	if err != nil {
		return State{}, fmt.Errorf("schtasks /Create /TN %s: %w%s", winTaskName, err, winOutputSuffix(out))
	}
	detail := "Task Scheduler logon task created (least privilege, hidden); " +
		"runs `vender daemon` at logon. A short console window can still appear because vender is a console-subsystem binary"
	if o.TUN {
		detail = "Task Scheduler logon task created with the highest available privileges (hidden) because TUN mode is on; " +
			"the daemon can create the WinTun adapter at logon without another prompt. " +
			"A short console window can still appear because vender is a console-subsystem binary"
	}
	if b.taskExe != "" {
		detail = "Task Scheduler logon task created using the GUI companion vender-daemon.exe; daemon and engine inherit a console hidden at creation; registration does not start the task"
		if o.TUN {
			detail += "; HighestAvailable is retained for TUN"
		}
	}
	return State{
		Enabled:   true,
		Mechanism: MechanismWindowsTask,
		Target:    winTaskName,
		Detail:    detail,
	}, nil
}

func (b windowsBackend) enableRunKey() (State, error) {
	return b.enableRunKeyWith(Options{})
}

func (b windowsBackend) enableRunKeyWith(o Options) (State, error) {
	if _, err := b.lookPath("reg"); err != nil {
		return State{}, errors.New("reg.exe is not available")
	}
	value := `"` + b.exe + `" ` + windowsDaemonArgs(o)
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

	if b.deleteTask != nil {
		firstErr = b.deleteTask()
	} else if _, err := b.lookPath("schtasks"); err == nil {
		if out, err := b.run("schtasks", "/Delete", "/TN", winTaskName, "/F"); err != nil && !winNotFound(out) {
			if firstErr == nil {
				firstErr = fmt.Errorf("schtasks /Delete /TN %s: %w%s", winTaskName, err, winOutputSuffix(out))
			}
		}
	}
	if b.deleteRunKey != nil {
		if err := b.deleteRunKey(); err != nil && firstErr == nil {
			firstErr = err
		}
	} else if _, err := b.lookPath("reg"); err == nil {
		if out, err := b.run("reg", "delete", winRunKeyPath, "/v", winRunValueName, "/f"); err != nil && !winNotFound(out) {
			if firstErr == nil {
				firstErr = fmt.Errorf("reg delete %s\\%s: %w%s", winRunKeyPath, winRunValueName, err, winOutputSuffix(out))
			}
		}
	}
	return firstErr
}

// --- status -----------------------------------------------------------------

func (b windowsBackend) status() (State, error) { return b.statusWith(Options{}) }

func (b windowsBackend) statusWith(o Options) (State, error) {
	if _, err := b.lookPath("schtasks"); err == nil {
		if out, err := b.run("schtasks", "/Query", "/TN", winTaskName, "/XML"); err == nil {
			if looksLikeTaskXML(out) {
				detail := "Task Scheduler logon task " + winTaskName + " is registered" + describeTaskCommand(out)
				if strings.Contains(strings.ToLower(normalizeWinOutput(out)), "vender-daemon.exe</command>") {
					detail += "; GUI companion selected for silent startup"
				} else {
					detail += "; console fallback: Hidden hides the task entry, not the console window"
				}
				if o.TUN {
					if taskIsElevated(out) {
						detail += "; it runs with the highest available privileges, which is what TUN mode needs"
					} else {
						detail += "; TUN is enabled but this task runs with the least privilege, which cannot create a WinTun adapter — " +
							"re-register it from an elevated prompt: `vender autostart on`"
					}
				}
				return State{
					Enabled:   true,
					Mechanism: MechanismWindowsTask,
					Target:    winTaskName,
					Detail:    detail,
				}, nil
			}
		}
	}
	if b.taskExists != nil {
		exists, err := b.taskExists()
		if err != nil {
			return State{}, err
		}
		if exists {
			return State{}, errors.New("a Windows logon task exists, but its XML could not be inspected")
		}
	}

	if _, err := b.lookPath("reg"); err == nil {
		if value, ok := b.runKeyValue(); ok {
			detail := "per-user Registry Run key = " + value
			if o.TUN {
				detail += "; TUN is enabled but a Run key value cannot run elevated, so the TUN adapter cannot be created — " +
					"remove it and run `vender autostart on` from an elevated prompt to register the logon task instead"
			}
			return State{
				Enabled:   true,
				Mechanism: MechanismWindowsRunKey,
				Target:    `HKCU\` + winRunKeyPath + `\` + winRunValueName,
				Detail:    detail,
			}, nil
		}
	}

	// Nothing installed: say what `on` would do.
	if _, err := b.lookPath("schtasks"); err == nil {
		detail := "not registered; `vender autostart on` would create a Task Scheduler logon task (falling back to the per-user Run key if that is refused)"
		if o.TUN {
			detail = "not registered; with TUN enabled `vender autostart on` creates the logon task with the highest available " +
				"privileges, which needs to be run once from an elevated prompt"
		}
		return State{
			Enabled:   false,
			Mechanism: MechanismWindowsTask,
			Target:    winTaskName,
			Detail:    detail,
		}, nil
	}
	return State{
		Enabled:   false,
		Mechanism: MechanismWindowsRunKey,
		Target:    `HKCU\` + winRunKeyPath + `\` + winRunValueName,
		Detail:    "not registered; `vender autostart on` would set the per-user Registry Run key",
	}, nil
}

// taskIsElevated reports whether a queried task definition runs with the
// highest available privileges.
func taskIsElevated(out string) bool {
	return strings.Contains(normalizeWinOutput(out), "<RunLevel>HighestAvailable</RunLevel>")
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
// /XML`. It is a logon trigger that runs in the interactive user's session, as
// a hidden task, at the least privilege by default and with the highest
// available privileges when TUN mode needs them (which also means the
// registration itself has to come from an elevated prompt).
//
// Note on the console window: `<Hidden>true</Hidden>` removes the task from the
// Task Scheduler library view and suppresses the default window in the common
// case, but a console-subsystem executable can still flash a console. The
// guaranteed windowless variant is a GUI-subsystem build or a `conhost
// --headless` launcher, both deferred to M7; the daemon itself needs no
// console to work.
func taskXML(exe string, elevated bool) string {
	return taskXMLWith(exe, Options{TUN: elevated}, "")
}

func taskXMLWith(exe string, o Options, sid string) string {
	runLevel := "LeastPrivilege"
	if o.TUN {
		runLevel = "HighestAvailable"
	}
	user := ""
	if sid != "" {
		user = "      <UserId>" + xmlEscape(sid) + "</UserId>\n"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>` + "\n" +
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n" +
		`  <RegistrationInfo>` + "\n" +
		`    <Description>vender proxy client: start the daemon at logon</Description>` + "\n" +
		`    <URI>` + xmlEscape(winTaskName) + `</URI>` + "\n" +
		`  </RegistrationInfo>` + "\n" +
		`  <Triggers>` + "\n" +
		`    <LogonTrigger>` + "\n" +
		`      <Enabled>true</Enabled>` + "\n" +
		user +
		`    </LogonTrigger>` + "\n" +
		`  </Triggers>` + "\n" +
		`  <Principals>` + "\n" +
		`    <Principal id="Author">` + "\n" +
		user +
		`      <LogonType>InteractiveToken</LogonType>` + "\n" +
		`      <RunLevel>` + runLevel + `</RunLevel>` + "\n" +
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
		`    <RestartOnFailure><Interval>PT1M</Interval><Count>5</Count></RestartOnFailure>` + "\n" +
		`    <Priority>7</Priority>` + "\n" +
		`  </Settings>` + "\n" +
		`  <Actions Context="Author">` + "\n" +
		`    <Exec>` + "\n" +
		`      <Command>` + xmlEscape(exe) + `</Command>` + "\n" +
		`      <Arguments>` + xmlEscape(windowsDaemonArgs(o)) + `</Arguments>` + "\n" +
		`      <WorkingDirectory>` + xmlEscape(windowsDir(exe)) + `</WorkingDirectory>` + "\n" +
		`    </Exec>` + "\n" +
		`  </Actions>` + "\n" +
		`</Task>` + "\n"
}

func windowsDaemonArgs(o Options) string {
	args := []string{"daemon"}
	if o.ConfigDir != "" {
		args = append(args, "--config-dir", quoteWindowsArg(o.ConfigDir))
	}
	if o.RunDir != "" {
		args = append(args, "--run-dir", quoteWindowsArg(o.RunDir))
	}
	return strings.Join(args, " ")
}

// Match CommandLineToArgvW/Go's command-line quoting, including a trailing
// backslash in a quoted directory. No shell is involved.
func quoteWindowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		if r == '\\' {
			slashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", 2*slashes+1))
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
		}
		b.WriteRune(r)
		slashes = 0
	}
	b.WriteString(strings.Repeat("\\", 2*slashes))
	b.WriteByte('"')
	return b.String()
}

func windowsDir(exe string) string {
	if i := strings.LastIndexAny(exe, `\/`); i >= 0 {
		return exe[:i+1]
	}
	return "."
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
	if !strings.ContainsRune(s, 0) && !strings.HasPrefix(s, "\xff\xfe") && !strings.HasPrefix(s, "\xfe\xff") {
		return strings.TrimPrefix(s, "\xef\xbb\xbf")
	}
	b := []byte(s)
	var order binary.ByteOrder = binary.LittleEndian
	if strings.HasPrefix(s, "\xfe\xff") {
		order = binary.BigEndian
		b = b[2:]
	} else if strings.HasPrefix(s, "\xff\xfe") {
		b = b[2:]
	} else if len(b) > 1 && b[0] == 0 {
		order = binary.BigEndian
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, order.Uint16(b[i:i+2]))
	}
	return string(utf16.Decode(units))
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
