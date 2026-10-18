//go:build windows

package autostart

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCommandArgumentsRoundTrip(t *testing.T) {
	o := Options{ConfigDir: `C:\中文 目录\config\`, RunDir: `C:\缓存 "quoted"\`}
	args, err := windows.DecomposeCommandLine("vender.exe " + windowsDaemonArgs(o))
	want := []string{"vender.exe", "daemon", "--config-dir", o.ConfigDir, "--run-dir", o.RunDir}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("native command-line parsing: %v %q", err, args)
	}
}

// Parsing an in-memory task definition validates the real Scheduler schema,
// without registering/deleting any task or changing startup state.
func TestWindowsSchedulerAcceptsGeneratedXML(t *testing.T) {
	b := defaultWindowsBackend()
	if b.identityErr != nil {
		t.Fatal(b.identityErr)
	}
	path := filepath.Join(t.TempDir(), "task.xml")
	xml := taskXMLWith(`C:\Program Files\vender\vender.exe`, Options{ConfigDir: `C:\中文 配置\`, RunDir: `C:\中文 缓存\`}, b.userSID)
	if err := os.WriteFile(path, utf16leBytes(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `$ErrorActionPreference='Stop'; $s=New-Object -ComObject 'Schedule.Service'; $s.Connect(); $t=$s.NewTask(0); $t.XmlText=[IO.File]::ReadAllText('` + strings.ReplaceAll(path, "'", "''") + `'); if(-not $t.XmlText){exit 1}`
	encoded := base64.StdEncoding.EncodeToString(utf16leBytes(script)[2:])
	out, err := runNativeWindowsCommand("powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
	if err != nil {
		t.Fatalf("Scheduler rejected XML: %v\n%s", err, normalizeWinOutput(out))
	}
}
