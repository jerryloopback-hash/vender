package autostart

import (
	"errors"
	"strings"
	"testing"
)

func TestTaskPrefersGUICompanionAndPreservesPrivilege(t *testing.T) {
	b := winBackend(newWinFake(), "schtasks", "reg")
	b.taskExe = `C:\用户 甲\vender\vender-daemon.exe`
	b.userSID = "S-1-5-21-123"
	var xml string
	b.writeTemp = func(s string) (string, error) { xml = s; return "", nil }
	st, err := b.enableWith(Options{TUN: true, ConfigDir: `C:\配置`, RunDir: `C:\缓存`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{b.taskExe, "HighestAvailable", b.userSID, "daemon --config-dir", "--run-dir"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if !strings.Contains(st.Detail, "GUI companion") || !strings.Contains(st.Detail, "does not start") {
		t.Fatal("incorrect startup claim")
	}
}

func TestInvalidCompanionDoesNotRegisterFallback(t *testing.T) {
	f := newWinFake()
	b := winBackend(f, "schtasks", "reg")
	b.launcherErr = errors.New("companion not GUI")
	if _, err := b.enable(); err == nil || len(f.calls) != 0 {
		t.Fatal("invalid companion changed autostart")
	}
}
