package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func init() {
	// Bubbles table truncates with runewidth's process-wide default, while
	// Lip Gloss measures with x/ansi. Without an explicit RUNEWIDTH_EASTASIAN,
	// runewidth infers CJK width from the locale/console but x/ansi stays narrow.
	// Follow the initialized ANSI policy once, before models or rendering
	// goroutines exist. Replace the condition so no old width LUT is retained.
	// This also sets the default for legacy runewidth users in non-TUI CLI
	// commands importing this package; no environment variables are changed.
	runewidth.EastAsianWidth = lipgloss.Width("…") == 2
	runewidth.DefaultCondition = runewidth.NewCondition()
}
