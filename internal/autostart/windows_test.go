package autostart

import (
	"errors"
	"strings"
	"testing"
)

// These tests run on any platform: the Windows mechanism is pure argument
// construction and output parsing, plus an injected runner. Only the real
// schtasks/reg invocation is left to the Windows build.

type winFake struct {
	calls   []string
	answers map[string]string
	fail    map[string]bool
}

func newWinFake() *winFake {
	return &winFake{answers: map[string]string{}, fail: map[string]bool{}}
}

func (f *winFake) run(name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	if f.fail[key] {
		return f.answers[key], errors.New("exit status 1")
	}
	return f.answers[key], nil
}

func (f *winFake) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func winBackend(f *winFake, tools ...string) windowsBackend {
	look := func(name string) (string, error) {
		for _, t := range tools {
			if t == name {
				return `C:\Windows\System32\` + name + ".exe", nil
			}
		}
		return "", errors.New("not found")
	}
	return windowsBackend{
		exe:       `C:\Program Files\vender\vender.exe`,
		run:       f.run,
		lookPath:  look,
		writeTemp: func(string) (string, error) { return "", nil },
	}
}

func TestWindowsEnableCreatesATaskAndFallsBackOnlyOnFailure(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")

	st, err := b.enable()
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !st.Enabled || st.Mechanism != MechanismWindowsTask || st.Target != winTaskName {
		t.Errorf("state = %+v", st)
	}
	if !f.ran("schtasks /Create /TN") {
		t.Errorf("the task was not created (ran: %v)", f.calls)
	}
	if f.ran("reg add") {
		t.Errorf("the Run key was written even though the task succeeded: %v", f.calls)
	}
}

func TestWindowsEnableFallsBackToTheRunKey(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	f.fail["schtasks /Create /TN "+winTaskName+" /XML  /F"] = true
	f.answers["schtasks /Create /TN "+winTaskName+" /XML  /F"] = "ERROR: Access is denied."

	st, err := b.enable()
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if st.Mechanism != MechanismWindowsRunKey {
		t.Fatalf("mechanism = %q, want the Run key fallback", st.Mechanism)
	}
	if !strings.HasPrefix(st.Target, `HKCU\`) || !strings.HasSuffix(st.Target, winRunValueName) {
		t.Errorf("target = %q", st.Target)
	}
	if !f.ran("reg add " + winRunKeyPath) {
		t.Errorf("the Run key was not written (ran: %v)", f.calls)
	}
	if !strings.Contains(st.Detail, "Access is denied") {
		t.Errorf("detail should explain why the task was skipped: %q", st.Detail)
	}

	// And that fallback must really parse back.
	f.answers["reg query "+winRunKeyPath+" /v "+winRunValueName] =
		`HKEY_CURRENT_USER\` + winRunKeyPath + "\r\n    " + winRunValueName + `    REG_SZ    "C:\Program Files\vender\vender.exe" daemon` + "\r\n"
	st2, err := b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st2.Enabled || st2.Mechanism != MechanismWindowsRunKey {
		t.Errorf("status = %+v", st2)
	}
}

func TestWindowsEnableWithoutSchtasksUsesTheRunKey(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "reg")
	st, err := b.enable()
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if st.Mechanism != MechanismWindowsRunKey {
		t.Errorf("mechanism = %q", st.Mechanism)
	}
}

func TestWindowsStatusReportsARegisteredTask(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	// schtasks writes UTF-16LE to a pipe; the fake reproduces that so the
	// parser is exercised against the real shape.
	f.answers["schtasks /Query /TN "+winTaskName+" /XML"] =
		utf16leString(taskXML(b.exe, false))

	st, err := b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Enabled || st.Mechanism != MechanismWindowsTask {
		t.Fatalf("state = %+v", st)
	}
	if !strings.Contains(st.Detail, "vender.exe") {
		t.Errorf("detail should name the executable the task runs: %q", st.Detail)
	}
}

func TestWindowsStatusWhenNothingIsRegistered(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	st, err := b.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Enabled {
		t.Errorf("state = %+v", st)
	}
	if st.Mechanism != MechanismWindowsTask || st.Target != winTaskName {
		t.Errorf("a disabled status should name the mechanism it would use: %+v", st)
	}
}

func TestWindowsDisableRemovesBothMechanismsAndIsIdempotent(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	if err := b.disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !f.ran("schtasks /Delete /TN") || !f.ran("reg delete ") {
		t.Errorf("disable did not try to remove both entries (ran: %v)", f.calls)
	}

	// A "not found" failure is success: `off` must be idempotent.
	f2 := newWinFake()
	b2 := winBackend(f2, "schtasks", "reg")
	f2.fail["schtasks /Delete /TN "+winTaskName+" /F"] = true
	f2.answers["schtasks /Delete /TN "+winTaskName+" /F"] = "ERROR: The system cannot find the file specified."
	f2.fail["reg delete "+winRunKeyPath+" /v "+winRunValueName+" /f"] = true
	f2.answers["reg delete "+winRunKeyPath+" /v "+winRunValueName+" /f"] =
		"ERROR: The system was unable to find the specified registry key or value."
	if err := b2.disable(); err != nil {
		t.Errorf("disabling what is not there = %v, want nil", err)
	}
}

func TestWindowsDisableReportsARealFailure(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	f.fail["schtasks /Delete /TN "+winTaskName+" /F"] = true
	f.answers["schtasks /Delete /TN "+winTaskName+" /F"] = "ERROR: Access is denied."
	if err := b.disable(); err == nil {
		t.Error("a genuine failure was swallowed")
	}
}

func TestTaskXMLShape(t *testing.T) {
	xml := taskXML(`C:\Program Files\vender\vender.exe`, false)
	for _, want := range []string{
		`encoding="UTF-16"`,
		`<URI>\vender\daemon</URI>`,
		"<LogonTrigger>",
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<Hidden>true</Hidden>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<Command>C:\\Program Files\\vender\\vender.exe</Command>",
		"<Arguments>daemon</Arguments>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML is missing %q:\n%s", want, xml)
		}
	}
	// XML metacharacters in the path must be escaped, not injected.
	if strings.Contains(taskXML(`C:\a&b<c>"d.exe`, false), `&b<c>`) {
		t.Error("the executable path is not XML-escaped")
	}
}

func TestUTF16EncodingHasABOMAndRoundTrips(t *testing.T) {
	b := utf16leBytes("abc")
	want := []byte{0xFF, 0xFE, 'a', 0x00, 'b', 0x00, 'c', 0x00}
	if len(b) != len(want) {
		t.Fatalf("length = %d, want %d", len(b), len(want))
	}
	for i := range want {
		if b[i] != want[i] {
			t.Fatalf("byte %d = %#x, want %#x", i, b[i], want[i])
		}
	}
}

func TestRunKeyValueQuotesThePath(t *testing.T) {
	if got := runKeyValue(`C:\Program Files\vender\vender.exe`); got != `"C:\Program Files\vender\vender.exe" daemon` {
		t.Errorf("runKeyValue = %q", got)
	}
}

func TestParseRegQuery(t *testing.T) {
	out := "HKEY_CURRENT_USER\\" + winRunKeyPath + "\r\n" +
		"    " + winRunValueName + `    REG_SZ    "C:\vender.exe" daemon` + "\r\n\r\n"
	value, ok := parseRegQuery(out)
	if !ok {
		t.Fatal("parseRegQuery did not find the value")
	}
	if value != `"C:\vender.exe" daemon` {
		t.Errorf("value = %q", value)
	}
	if _, ok := parseRegQuery("HKEY_CURRENT_USER\\" + winRunKeyPath + "\r\n"); ok {
		t.Error("parseRegQuery found a value in a header-only output")
	}
}

func TestNormalizeWinOutputStripsUTF16NULs(t *testing.T) {
	utf16 := "\x00T\x00a\x00s\x00k\x00"
	if got := normalizeWinOutput(utf16); got != "Task" {
		t.Errorf("normalizeWinOutput = %q", got)
	}
	if got := normalizeWinOutput("plain"); got != "plain" {
		t.Errorf("normalizeWinOutput changed plain text: %q", got)
	}
}

func TestWinNotFoundMarkers(t *testing.T) {
	if !winNotFound("ERROR: The system cannot find the file specified.") {
		t.Error("a missing task was not recognised as benign")
	}
	if !winNotFound("ERROR: The system was unable to find the specified registry key or value.") {
		t.Error("a missing registry value was not recognised as benign")
	}
	if winNotFound("ERROR: Access is denied.") {
		t.Error("an access denial was mistaken for 'not found'")
	}
}

// utf16leString renders s as the UTF-16LE text schtasks would print.
func utf16leString(s string) string {
	return string(utf16leBytes(s))
}

// --- TUN mode: the elevated logon task --------------------------------------

// With TUN enabled the task must run with the highest available privileges:
// a WinTun adapter is a driver-level device, and a LeastPrivilege task cannot
// create one.
func TestEnableWithTunCreatesAnElevatedTask(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	st, err := b.enableWith(Options{TUN: true})
	if err != nil {
		t.Fatalf("enableWith: %v", err)
	}
	if !st.Enabled || st.Mechanism != MechanismWindowsTask {
		t.Errorf("state = %+v", st)
	}
	if !strings.Contains(st.Detail, "highest available privileges") {
		t.Errorf("detail = %q", st.Detail)
	}
	if !strings.Contains(taskXML(b.exe, true), "<RunLevel>HighestAvailable</RunLevel>") {
		t.Error("the staged task XML does not ask for the highest run level")
	}
}

// The per-user Run key cannot elevate, so a TUN registration must not fall back
// to it: installing something that cannot work is worse than an error.
func TestEnableWithTunDoesNotFallBackToTheRunKey(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	f.fail["schtasks /Create /TN "+winTaskName+" /XML  /F"] = true
	f.answers["schtasks /Create /TN "+winTaskName+" /XML  /F"] = "ERROR: Access is denied."

	_, err := b.enableWith(Options{TUN: true, CapabilityHint: "this process is not elevated"})
	if err == nil {
		t.Fatal("enableWith reported success although the task could not be created")
	}
	if !strings.Contains(err.Error(), "elevated") {
		t.Errorf("error does not say elevation is needed: %v", err)
	}
	if !strings.Contains(err.Error(), "not elevated") {
		t.Errorf("error does not quote the privilege verdict: %v", err)
	}
	if f.ran("reg add ") {
		t.Errorf("the Run key was written for a TUN registration (calls: %v)", f.calls)
	}
}

// Without TUN the least-privilege task and the Run key fallback are unchanged.
func TestEnableWithoutTunKeepsLeastPrivilege(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	st, err := b.enableWith(Options{})
	if err != nil {
		t.Fatalf("enableWith: %v", err)
	}
	if st.Mechanism != MechanismWindowsTask {
		t.Errorf("mechanism = %q", st.Mechanism)
	}
	if !strings.Contains(taskXML(b.exe, false), "<RunLevel>LeastPrivilege</RunLevel>") {
		t.Error("the default task is not least-privilege")
	}
}

// status must say when the registered task cannot deliver what TUN needs.
func TestWindowsStatusWithTun(t *testing.T) {
	t.Run("elevated task", func(t *testing.T) {
		f := newWinFake()
		b := winBackend(f, "schtasks", "reg")
		f.answers["schtasks /Query /TN "+winTaskName+" /XML"] = utf16leString(taskXML(b.exe, true))
		st, err := b.statusWith(Options{TUN: true})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		if !strings.Contains(st.Detail, "which is what TUN mode needs") {
			t.Errorf("detail = %q", st.Detail)
		}
	})

	t.Run("least-privilege task while TUN is on", func(t *testing.T) {
		f := newWinFake()
		b := winBackend(f, "schtasks", "reg")
		f.answers["schtasks /Query /TN "+winTaskName+" /XML"] = utf16leString(taskXML(b.exe, false))
		st, err := b.statusWith(Options{TUN: true})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		for _, want := range []string{"cannot create a WinTun adapter", "vender autostart on"} {
			if !strings.Contains(st.Detail, want) {
				t.Errorf("detail %q is missing %q", st.Detail, want)
			}
		}
	})

	t.Run("run key while TUN is on", func(t *testing.T) {
		f := newWinFake()
		b := winBackend(f, "reg")
		f.answers["reg query "+winRunKeyPath+" /v "+winRunValueName] =
			winRunKeyPath + "\r\n    " + winRunValueName + "    REG_SZ    \"C:\\vender\\vender.exe\" daemon\r\n"
		st, err := b.statusWith(Options{TUN: true})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		if !strings.Contains(st.Detail, "cannot run elevated") {
			t.Errorf("detail = %q", st.Detail)
		}
	})

	t.Run("nothing registered", func(t *testing.T) {
		f := newWinFake()
		b := winBackend(f, "schtasks", "reg")
		st, err := b.statusWith(Options{TUN: true})
		if err != nil {
			t.Fatalf("statusWith: %v", err)
		}
		if st.Enabled || !strings.Contains(st.Detail, "highest available") {
			t.Errorf("state = %+v", st)
		}
	})
}
