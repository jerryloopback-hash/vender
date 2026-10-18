package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/ll-lite/vender/internal/control"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{900 * 1024, "900 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{12 * 1024 * 1024, "12.00 MiB"},
		{1024 * 1024 * 1024, "1.00 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := humanRate(1536); got != "1.50 KiB/s" {
		t.Errorf("humanRate = %q", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int]string{
		0: "0s", 12: "12s", 59: "59s",
		60: "1m00s", 192: "3m12s",
		3600: "1h00m", 7385: "2h03m",
		86400: "1d00h", 100000: "1d03h",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanMillis(t *testing.T) {
	cases := map[int]string{0: "0 ms", 38: "38 ms", 924: "924 ms", 1500: "1.5 s", 12345: "12.3 s"}
	for in, want := range cases {
		if got := humanMillis(in); got != want {
			t.Errorf("humanMillis(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestGraphScalesAndAligns(t *testing.T) {
	// An empty history is a blank row rather than a misleading flat line.
	if got := graph(nil, 5); got != "     " {
		t.Errorf("graph(nil) = %q, want five spaces", got)
	}
	// A single sample sits at the right edge, so "now" is always the newest
	// column nearest the readout.
	if got := graph([]float64{0}, 4); got != "   ▁" {
		t.Errorf("graph(1 sample) = %q", got)
	}
	// The peak reaches the top level and a zero sample the bottom level.
	got := graph([]float64{0, 2048}, 2)
	if got != "▁█" {
		t.Errorf("graph = %q, want ▁█", got)
	}
	// Only the most recent samples are drawn when there is more history than
	// width.
	long := make([]float64, 50)
	for i := range long {
		long[i] = float64(i + 1)
	}
	if got := graph(long, 3); len([]rune(got)) != 3 {
		t.Errorf("graph width = %d runes, want 3", len([]rune(got)))
	}
	// Below the floor everything is a flat trace rather than a full-height
	// bar, so an idle connection does not look busy.
	if got := graph([]float64{12, 12}, 2); got != "▁▁" {
		t.Errorf("graph(tiny) = %q, want a flat trace", got)
	}
}

func TestTruncateAndPadUseDisplayWidth(t *testing.T) {
	for _, tt := range []struct {
		s            string
		width        int
		narrow, wide string
	}{
		{"abcdef", -1, "", ""},
		{"abcdef", 0, "", ""},
		{"abcdef", 1, "…", ""},
		{"abcdef", 2, "a…", "…"},
		{"abcdef", 3, "ab…", "a…"},
		{"中文ab", 3, "中…", "…"},
		{"中文ab", 4, "中…", "中…"},
	} {
		want := tt.narrow
		if lipgloss.Width("…") == 2 {
			want = tt.wide
		}
		if got := truncate(tt.s, tt.width); got != want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.width, got, want)
		}
	}
	for _, s := range []string{"", "abcdef", "中文ab", "e\u0301cole", "🙂abc", "☀️abc", "…abc"} {
		for width := 0; width <= 10; width++ {
			if got := truncate(s, width); lipgloss.Width(got) > width {
				t.Errorf("truncate(%q, %d) = %q exceeds display width", s, width, got)
			}
			if got := pad(s, width); lipgloss.Width(got) != width {
				t.Errorf("pad(%q, %d) = %q has width %d", s, width, got, lipgloss.Width(got))
			}
		}
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Errorf("truncate(short) = %q", got)
	}
	// A double-width rune must not push the padding off.
	if got := pad("中文", 4); got != "中文" {
		t.Errorf("pad(cjk) = %q, want no extra padding", got)
	}
	if got := pad("ab", 4); got != "ab  " {
		t.Errorf("pad = %q", got)
	}
	if n := len([]rune(pad("hi", 10))); n != 10 {
		t.Errorf("padded length = %d runes", n)
	}
}

func TestThemePanelIsExactWidth(t *testing.T) {
	th := newTheme(nil)
	for _, width := range []int{16, 17, 40, 41, 60, 100} {
		for _, title := range []string{"", "title", "中文", "a very long title that cannot possibly fit"} {
			got := th.panel(title, width, "a line", "another")
			for i, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w != width {
					t.Errorf("width %d, title %q: line %d is %d columns: %q", width, title, i, w, line)
				}
			}
		}
	}
}

func TestLatencyText(t *testing.T) {
	cases := []struct {
		in   control.LatencyData
		want string
	}{
		{control.LatencyData{Ms: 0, Err: "boom"}, "unmeasurable"},
		{control.LatencyData{Ms: 0}, "-"},
		{control.LatencyData{Ms: 924}, "924 ms"},
		{control.LatencyData{Ms: 38, Method: "icmp-direct", Sent: 3, Received: 2, PacketLoss: 33}, "38 ms/33%"},
	}
	for _, c := range cases {
		if got := latencyText(c.in); got != c.want {
			t.Errorf("latencyText(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}
