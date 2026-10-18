package tui

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// x/ansi reads RUNEWIDTH_EASTASIAN at init, so each mode needs a fresh
// process. Scrub inherited locale/width variables to cover both explicit modes
// and runewidth's locale inference when the width override is absent.
func TestDisplayWidthModes(t *testing.T) {
	const childEnv = "VENDER_TEST_WIDTH_CHILD"
	cases := []struct {
		name, locale, override string // empty override means absent, not set to ""
		width                  int
	}{
		{"unset-zh", "zh_CN.UTF-8", "", 1},
		{"unset-en", "en_US.UTF-8", "", 1},
		{"explicit-0", "zh_CN.UTF-8", "0", 1},
		{"explicit-1", "en_US.UTF-8", "1", 2},
	}
	if name := os.Getenv(childEnv); name != "" {
		for _, tt := range cases {
			if tt.name != name {
				continue
			}
			if value, present := os.LookupEnv("RUNEWIDTH_EASTASIAN"); value != tt.override || present != (tt.override != "") {
				t.Fatalf("%s: unexpected width override %q (present=%v)", name, value, present)
			}
			for _, key := range []string{"LANG", "LC_CTYPE"} {
				if value := os.Getenv(key); value != tt.locale {
					t.Fatalf("%s: %s=%q, want %q", name, key, value, tt.locale)
				}
			}
			if _, present := os.LookupEnv("LC_ALL"); present {
				t.Fatal("LC_ALL must be absent so it cannot mask the test locale")
			}
			for source, got := range map[string]int{
				"ANSI":                 lipgloss.Width("…"),
				"legacy table":         runewidth.StringWidth("…"),
				"new legacy condition": runewidth.NewCondition().StringWidth("…"),
			} {
				if got != tt.width {
					t.Errorf("%s: %s ellipsis width = %d, want %d", name, source, got, tt.width)
				}
			}
			return
		}
		t.Fatalf("unknown child environment %q", name)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"-test.run=^(TestDisplayWidthModes|TestView.*|TestTruncateAndPadUseDisplayWidth|TestThemePanelIsExactWidth)$", "-test.count=1"}
			if *update {
				args = append(args, "-update")
			}
			cmd := exec.Command(exe, args...)
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if !strings.EqualFold(key, "RUNEWIDTH_EASTASIAN") && !strings.EqualFold(key, childEnv) &&
					!strings.EqualFold(key, "LANG") && !strings.EqualFold(key, "LC_CTYPE") && !strings.EqualFold(key, "LC_ALL") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "LANG="+tt.locale, "LC_CTYPE="+tt.locale, childEnv+"="+tt.name)
			if tt.override != "" {
				cmd.Env = append(cmd.Env, "RUNEWIDTH_EASTASIAN="+tt.override)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("environment %s: %v\n%s", tt.name, err, output)
			}
		})
	}
}
