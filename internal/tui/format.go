package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// graphLevels are the eight block heights used by the traffic graph, from a
// trace to a full block. They are Unicode block elements (U+2581..U+2588),
// which render in every terminal font vender realistically targets, including
// Windows Terminal and the default Windows console font.
var graphLevels = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// graphFloor is the smallest value the graph auto-scale will use. Without it, a
// session that transfers 12 bytes per second would render exactly like a
// gigabit transfer, because everything is relative.
const graphFloor = 1024.0

// humanRate formats a byte-per-second rate with a binary unit, e.g.
// "1.24 MiB/s". Below one kilobyte per second it drops the decimal so an idle
// connection reads as "0 B/s" rather than "0.00 B/s".
func humanRate(bytesPerSec float64) string {
	return humanBytes(bytesPerSec) + "/s"
}

// humanBytes formats a byte count with a binary unit.
func humanBytes(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return "-"
	}
	neg := n < 0
	n = math.Abs(n)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	var s string
	switch {
	case i == 0:
		s = fmt.Sprintf("%.0f %s", n, units[i])
	case n >= 100:
		s = fmt.Sprintf("%.0f %s", n, units[i])
	default:
		s = fmt.Sprintf("%.2f %s", n, units[i])
	}
	if neg {
		return "-" + s
	}
	return s
}

// humanDuration renders an uptime the way a person reads a clock, not the way
// a duration type prints.
func humanDuration(secs int) string {
	if secs < 0 {
		return "-"
	}
	d := time.Duration(secs) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// graph renders one row of sparkline bars for values (oldest first), scaled so
// the row uses the full height available. width is the number of columns; when
// there is less data than width the row is right-aligned so the newest sample
// is always at the right edge, next to the "now" readout.
func graph(values []float64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	peak := graphFloor
	for _, v := range values {
		if v > peak {
			peak = v
		}
	}
	var b strings.Builder
	for i := 0; i < width-len(values); i++ {
		b.WriteRune(' ')
	}
	for _, v := range values {
		if v <= 0 {
			b.WriteRune(graphLevels[0])
			continue
		}
		// A logarithmic-ish scale would make small rates visible next to a
		// large peak, but it also makes the shape of a burst lie. A linear
		// scale against the window peak is what a person expects from a
		// sparkline.
		idx := int(math.Round(v / peak * float64(len(graphLevels)-1)))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(graphLevels) {
			idx = len(graphLevels) - 1
		}
		b.WriteRune(graphLevels[idx])
	}
	return b.String()
}

// truncate shortens s to at most width columns, adding an ellipsis when it had
// to. Width is measured in display columns (via lipgloss), so a server name
// containing double-width characters still lines up with the panels around it.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if lipgloss.Width(b.String())+lipgloss.Width(string(r)) > width-1 {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}

// pad right-pads s to width (truncating if needed) so columns line up.
func pad(s string, width int) string {
	s = truncate(s, width)
	if n := width - lipgloss.Width(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// peak returns the largest value in the slice (0 when empty).
func peak(values []float64) float64 {
	m := 0.0
	for _, v := range values {
		if v > m {
			m = v
		}
	}
	return m
}
