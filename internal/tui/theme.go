package tui

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// theme holds every styled fragment the TUI draws. Styles are built from a
// *lipgloss.Renderer so the colour decision is made in exactly one place:
//
//   - the program uses a renderer over os.Stdout, so a colour terminal gets
//     colour (and NO_COLOR takes it away, because termenv honours it);
//   - tests and the non-TTY fallback use a renderer over io.Discard, whose
//     profile is plain ASCII and whose styles are no-ops. The same View code
//     then produces clean, unstyled text — which is what makes the view tests
//     usable as golden tests.
//
// Box drawing is deliberately not conditional on colour: NO_COLOR means "no
// colour", not "no structure", and a plain-text panel is still readable.
type theme struct {
	renderer *lipgloss.Renderer
	noColor  bool

	appName   lipgloss.Style
	appTag    lipgloss.Style
	tab       lipgloss.Style
	tabActive lipgloss.Style
	border    lipgloss.Style
	title     lipgloss.Style
	subtle    lipgloss.Style
	value     lipgloss.Style
	accent    lipgloss.Style
	ok        lipgloss.Style
	warn      lipgloss.Style
	bad       lipgloss.Style
	dim       lipgloss.Style
	down      lipgloss.Style
	up        lipgloss.Style
	bar       lipgloss.Style
	toastOK   lipgloss.Style
	toastErr  lipgloss.Style
	rowActive lipgloss.Style
	key       lipgloss.Style
}

// newTheme builds the style set for a renderer. A nil renderer means "no
// colour" and is what tests pass.
func newTheme(r *lipgloss.Renderer) theme {
	if r == nil {
		r = lipgloss.NewRenderer(io.Discard)
	}
	// termenv.Profile.Name() is the only stable way to ask "is this profile
	// colourless?" without importing termenv just for one comparison.
	noColor := r.ColorProfile().Name() == "Ascii"
	return theme{
		renderer:  r,
		noColor:   noColor,
		appName:   r.NewStyle().Bold(true),
		appTag:    r.NewStyle().Faint(true),
		tab:       r.NewStyle().Foreground(lipgloss.Color("245")),
		tabActive: r.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
		border:    r.NewStyle().Foreground(lipgloss.Color("240")),
		title:     r.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
		subtle:    r.NewStyle().Foreground(lipgloss.Color("245")),
		value:     r.NewStyle().Foreground(lipgloss.Color("252")),
		accent:    r.NewStyle().Foreground(lipgloss.Color("212")),
		ok:        r.NewStyle().Foreground(lipgloss.Color("82")),
		warn:      r.NewStyle().Foreground(lipgloss.Color("220")),
		bad:       r.NewStyle().Foreground(lipgloss.Color("203")),
		dim:       r.NewStyle().Faint(true),
		down:      r.NewStyle().Foreground(lipgloss.Color("81")),
		up:        r.NewStyle().Foreground(lipgloss.Color("212")),
		bar:       r.NewStyle().Foreground(lipgloss.Color("63")),
		toastOK:   r.NewStyle().Foreground(lipgloss.Color("82")),
		toastErr:  r.NewStyle().Foreground(lipgloss.Color("203")),
		rowActive: r.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
		key:       r.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
	}
}

// panel draws a titled box of exactly the given outer width. Every screen is
// built from panels, so the layout stays aligned while the terminal resizes.
// Content lines are padded (and truncated) to the inner width, which is what
// keeps the right-hand border straight.
func (t theme) panel(title string, width int, lines ...string) string {
	const minWidth = 16
	if width < minWidth {
		width = minWidth
	}
	inner := width - 4 // inside the border, with one space of padding

	var b strings.Builder
	if title == "" {
		b.WriteString(t.border.Render("╭" + strings.Repeat("─", width-2) + "╮"))
	} else {
		fill := width - 5 - lipgloss.Width(title)
		if fill < 0 {
			title = truncate(title, max(1, width-6))
			fill = width - 5 - lipgloss.Width(title)
		}
		b.WriteString(t.border.Render("╭─ "))
		b.WriteString(t.title.Render(title))
		b.WriteString(t.border.Render(" " + strings.Repeat("─", fill) + "╮"))
	}
	for _, l := range lines {
		b.WriteString("\n")
		b.WriteString(t.border.Render("│"))
		b.WriteString(" ")
		b.WriteString(pad(l, inner))
		b.WriteString(" ")
		b.WriteString(t.border.Render("│"))
	}
	b.WriteString("\n")
	b.WriteString(t.border.Render("╰" + strings.Repeat("─", width-2) + "╯"))
	return b.String()
}

// statusDot renders a filled/hollow dot for a boolean state.
func (t theme) statusDot(on bool, onStyle, offStyle lipgloss.Style) string {
	if on {
		return onStyle.Render("●")
	}
	return offStyle.Render("○")
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
