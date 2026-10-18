package autostart

import (
	"errors"
	"strings"
	"testing"
)

func TestTaskPreservesDirectoriesUserAndRecovery(t *testing.T) {
	o := Options{TUN: true, ConfigDir: `C:\用户 甲\配置\`, RunDir: `C:\用户 甲\缓存`}
	xml := taskXMLWith(`C:\Program Files\vender\vender.exe`, o, "S-1-5-21-123")
	for _, want := range []string{`<UserId>S-1-5-21-123</UserId>`, `<RestartOnFailure>`, `<Interval>PT1M</Interval>`, `<Count>5</Count>`, `--config-dir`, `--run-dir`, `<RunLevel>HighestAvailable</RunLevel>`, `<WorkingDirectory>C:\Program Files\vender\</WorkingDirectory>`} {
		if !strings.Contains(xml, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Count(xml, "<UserId>") != 2 {
		t.Fatal("logon trigger and principal must both bind to the user")
	}
	args := windowsDaemonArgs(o)
	if !strings.Contains(args, `"C:\用户 甲\配置\\"`) {
		t.Fatalf("trailing backslash not escaped: %s", args)
	}
}

func TestNativeDeletionDoesNotDependOnLocalizedOutput(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	task, run := 0, 0
	b.deleteTask = func() error { task++; return nil }
	b.deleteRunKey = func() error { run++; return nil }
	if err := b.disable(); err != nil {
		t.Fatal(err)
	}
	if task != 1 || run != 1 || len(f.calls) != 0 {
		t.Fatal("native deletion used localized CLI text")
	}
	b.deleteTask = func() error { return errors.New("access denied, not absence") }
	if err := b.disable(); err == nil {
		t.Fatal("real failure swallowed")
	}
}

func TestTaskSuccessRemovesOldRunEntry(t *testing.T) {
	b := winBackend(newWinFake(), "schtasks", "reg")
	removed := false
	b.deleteRunKey = func() error { removed = true; return nil }
	if _, err := b.enable(); err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("old Run entry retained")
	}
}

func TestRefusedTaskUpdateDoesNotInstallConflictingRunKey(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	f.fail["schtasks /Create /TN "+winTaskName+" /XML  /F"] = true
	b.taskExists = func() (bool, error) { return true, nil }
	if _, err := b.enable(); err == nil {
		t.Fatal("refused update was hidden by fallback")
	}
	if f.ran("reg add") {
		t.Fatal("installed Run entry alongside existing task")
	}
}

func TestTUNTaskRegistrationRequiresActualElevation(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	b.isElevated = func() bool { return false }
	if _, err := b.enableWith(Options{TUN: true}); err == nil {
		t.Fatal("HighestAvailable was mistaken for actual elevation")
	}
	if len(f.calls) != 0 {
		t.Fatal("changed startup state without elevation")
	}
}

func TestUTF16ChineseOutputRoundTrips(t *testing.T) {
	want := `<Command>C:\用户\代理 客户端\vender.exe</Command>`
	if got := normalizeWinOutput(string(utf16leBytes(want))); got != want {
		t.Fatalf("UTF16 corrupted: %q", got)
	}
}
