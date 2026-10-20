package tui

import (
	"os"
	"regexp"
	"testing"
)

func TestMilestoneMatchesReadme(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("`vender` is at \\*\\*(M[0-9]+)\\*\\*").FindSubmatch(b)
	if len(m) != 2 || string(m[1]) != milestoneTag {
		t.Fatalf("TUI milestone %q does not match README Status", milestoneTag)
	}
}
