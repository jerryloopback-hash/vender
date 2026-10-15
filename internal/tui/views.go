package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
	"github.com/ll-lite/vender/internal/rules"
)

// panelWidth is the outer width every panel uses.
func (m Model) panelWidth() int {
	if m.width <= 0 {
		return 100
	}
	return max(24, m.width)
}

// View renders the whole screen. It is pure: everything it needs is in the
// model, which is what makes the view tests possible.
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "vender: measuring the terminal…"
	}
	header := m.tabsView()
	if m.showHelp {
		return header + "\n" + m.helpOverlay() + "\n" + m.footerView()
	}
	body := m.screenView()
	body = m.clipBody(body, m.height-lipgloss.Height(header)-2)
	return header + "\n" + body + "\n" + m.footerView()
}

// screenView dispatches to the active screen.
func (m Model) screenView() string {
	switch m.screen {
	case screenNodes:
		return m.nodesView()
	case screenLogs:
		return m.logsView()
	case screenSettings:
		return m.settingsView()
	default:
		return m.dashboardView()
	}
}

// --- chrome -----------------------------------------------------------------

// tabsView draws the tab bar with a highlight that slides from the previous
// screen to the current one over a few animation frames, which is what makes a
// screen change read as movement rather than a jump cut.
func (m Model) tabsView() string {
	seen := m.tabHighlightStart()
	lo := int(seen)
	hi := lo + tabCellWidth

	var b strings.Builder
	for i, name := range screenNames {
		cell := " " + pad(name, tabCellWidth-2) + " "
		for j, r := range cell {
			col := i*tabCellWidth + j
			if col >= lo && col < hi {
				b.WriteString(m.theme.tabActive.Render(string(r)))
			} else {
				b.WriteString(m.theme.tab.Render(string(r)))
			}
		}
	}
	right := m.theme.appName.Render("vender") + " " + m.theme.appTag.Render("M3")
	if gap := m.panelWidth() - lipgloss.Width(b.String()) - lipgloss.Width(right) - 1; gap > 0 {
		b.WriteString(repeat(" ", gap))
		b.WriteString(right)
	}
	return b.String()
}

// tabHighlightStart interpolates the highlight's left column between the
// previous screen and the current one.
func (m Model) tabHighlightStart() float64 {
	target := float64(int(m.screen) * tabCellWidth)
	if m.tabAnim >= tabAnimSteps {
		return target
	}
	from := float64(m.fromTab * tabCellWidth)
	// Ease out: fast at first, settling gently on the new tab.
	f := 1 - float64(tabAnimSteps-m.tabAnim)/float64(tabAnimSteps)
	f = 1 - (1-f)*(1-f)
	return from + (target-from)*f
}

// footerView is the two-line status area: what just happened, and the keys for
// the current screen.
func (m Model) footerView() string {
	return m.statusLine() + "\n" + m.helpLine()
}

func (m Model) statusLine() string {
	switch {
	case m.busy != "":
		return " " + m.spinner.View() + " " + m.theme.accent.Render(m.busy)
	case m.toast != "":
		if m.toastErr {
			return " " + m.theme.bad.Render("✗ "+m.toast)
		}
		return " " + m.theme.ok.Render("✓ "+m.toast)
	case m.attached:
		where := m.theme.subtle.Render("attached to the daemon at " + m.status.ClashListen)
		return " " + m.theme.ok.Render("●") + " " + m.theme.subtle.Render("daemon pid "+fmt.Sprint(m.status.PID)) + "  " + where
	default:
		return " " + m.theme.bad.Render("○ daemon not running") + " " +
			m.theme.subtle.Render("— start it with `vender daemon`") +
			m.theme.dim.Render("  ("+m.attachHint()+")")
	}
}

func (m Model) attachHint() string {
	if m.attachErr == "" {
		return "waiting"
	}
	return truncate(m.attachErr, 60)
}

func (m Model) helpLine() string {
	h := help.New()
	h.Width = m.panelWidth()
	if m.attached {
		h.Styles.ShortKey = m.theme.key
		h.Styles.ShortDesc = m.theme.subtle
		h.Styles.ShortSeparator = m.theme.dim
	}
	return " " + h.View(m.keyMap())
}

// clipBody trims the body to the available height, so a long policy list or a
// tiny terminal window never pushes the help line off screen.
func (m Model) clipBody(body string, avail int) string {
	if avail <= 0 {
		return ""
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= avail {
		return body
	}
	lines = lines[:avail]
	lines[avail-1] = m.theme.subtle.Render("… (resize the window, or press ? for help)")
	return strings.Join(lines, "\n")
}

// --- dashboard --------------------------------------------------------------

func (m Model) dashboardView() string {
	w := m.panelWidth()
	if !m.attached {
		return strings.Join([]string{
			m.daemonPanel(w),
			m.trafficPanel(w),
			m.nodesPanel(w),
		}, "\n")
	}
	return strings.Join([]string{
		m.daemonPanel(w),
		m.trafficPanel(w),
		m.nodesPanel(w),
	}, "\n")
}

func (m Model) daemonPanel(w int) string {
	if !m.attached {
		lines := []string{
			m.theme.statusDot(false, m.theme.ok, m.theme.bad) + " " +
				m.theme.bad.Render("daemon not running") + "  " +
				m.theme.subtle.Render("(nothing holds "+m.runDirLabel()+")"),
			"",
			"  " + m.theme.subtle.Render("start it with") + "   " + m.theme.value.Render("vender daemon"),
			"  " + m.theme.subtle.Render("run dir        ") + "   " + m.theme.value.Render(m.runDirLabel()),
			"",
			"  " + m.theme.dim.Render("The TUI needs no daemon to start: Nodes and Settings read your configuration."),
		}
		if m.attachErr != "" {
			lines = append(lines, "  "+m.theme.bad.Render(truncate(m.attachErr, max(20, w-8))))
		}
		return m.theme.panel("daemon", w, lines...)
	}

	st := m.status
	engineState := "running"
	engineStyle := m.theme.ok
	switch {
	case st.Restarting:
		engineState, engineStyle = "restarting", m.theme.warn
	case !st.EngineRunning:
		engineState, engineStyle = "stopped", m.theme.bad
	case !st.EngineHealthy:
		engineState, engineStyle = "listening", m.theme.warn
	}

	line1 := m.theme.statusDot(true, m.theme.ok, m.theme.bad) + " " +
		m.theme.value.Render("daemon running") + m.theme.subtle.Render("  pid ") + m.theme.value.Render(fmt.Sprint(st.PID)) +
		m.theme.subtle.Render("  ·  engine ") + engineStyle.Render(engineState) +
		m.theme.subtle.Render("  pid ") + m.theme.value.Render(pidText(st.EnginePid)) +
		m.theme.subtle.Render("  ·  uptime ") + m.theme.value.Render(uptimeText(st)) +
		m.theme.subtle.Render("  ·  v") + m.theme.value.Render(m.shortVersion(st.Vender))

	line2 := m.theme.subtle.Render("server   ") + m.serverText()
	line3 := m.theme.subtle.Render("mode     ") + m.theme.value.Render(orDash(st.Mode)) +
		m.theme.subtle.Render("   listen ") + m.theme.value.Render(orDash(st.Listen)) +
		m.theme.subtle.Render("   servers ") + m.theme.value.Render(fmt.Sprintf("%d (%d active)", st.ServerCount, activeCount(m.servers)))
	line4 := m.theme.subtle.Render("latency  ") + m.latencyBadge() +
		m.theme.subtle.Render("   engine ") + m.theme.value.Render(orDash(st.EngineVersion))

	lines := []string{line1, line2, line3, line4}
	if st.LastError != "" {
		lines = append(lines, m.theme.bad.Render("last error: "+truncate(st.LastError, max(20, w-14))))
	}
	return m.theme.panel("daemon", w, lines...)
}

func (m Model) trafficPanel(w int) string {
	if !m.attached {
		return m.theme.panel("traffic", w,
			m.theme.subtle.Render("no live traffic: the engine is not running"),
			m.theme.dim.Render("traffic counters come from the engine's Clash API, which only exists while it runs"),
		)
	}
	inner := w - 4
	graphWidth := max(0, inner-20)
	down := graph(m.downRates, graphWidth)
	up := graph(m.upRates, graphWidth)

	cur := m.currentDown()
	pk := peak(m.downRates)
	load := m.progress.ViewAs(ratio(cur, pk))

	lines := []string{
		m.theme.subtle.Render("down  ") + m.theme.down.Render(pad(humanRate(cur), 12)) + m.theme.down.Render(down),
		m.theme.subtle.Render("up    ") + m.theme.up.Render(pad(humanRate(m.currentUp()), 12)) + m.theme.up.Render(up),
		m.theme.subtle.Render("load  ") + m.theme.bar.Render(load) + "  " + m.theme.subtle.Render("peak "+humanRate(pk)),
		m.theme.subtle.Render("total ") + m.theme.down.Render("↓ "+humanBytes(float64(m.totalDown))) +
			"  " + m.theme.up.Render("↑ "+humanBytes(float64(m.totalUp))) +
			m.theme.dim.Render(fmt.Sprintf("   (%d samples, 1/s)", len(m.downRates))),
	}
	return m.theme.panel("traffic", w, lines...)
}

func (m Model) nodesPanel(w int) string {
	active := activeCount(m.servers)
	label := fmt.Sprintf("%d configured, %d active", len(m.servers), active)
	if len(m.servers) == 0 {
		label = "none configured"
	}
	return m.theme.panel("nodes", w,
		m.theme.value.Render(label)+m.theme.subtle.Render("   ·   press 2 or tab for the list"),
	)
}

func (m Model) currentDown() float64 {
	if len(m.downRates) == 0 {
		return 0
	}
	return m.downRates[len(m.downRates)-1]
}

func (m Model) currentUp() float64 {
	if len(m.upRates) == 0 {
		return 0
	}
	return m.upRates[len(m.upRates)-1]
}

func ratio(v, maxV float64) float64 {
	if maxV <= 0 {
		return 0
	}
	r := v / maxV
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func activeCount(rows []serverRow) int {
	n := 0
	for _, r := range rows {
		if r.Active {
			n++
		}
	}
	return n
}

func (m Model) serverText() string {
	if m.currentMode() == string(config.ModeDirect) {
		return m.theme.dim.Render("(not used in direct mode)")
	}
	name := m.selectedName()
	if name == "" {
		return m.theme.bad.Render("(none selected)")
	}
	for _, r := range m.servers {
		if strings.EqualFold(r.Name, name) {
			return m.theme.value.Render(name) + m.theme.subtle.Render("  "+r.Type+"  "+r.Endpoint)
		}
	}
	return m.theme.value.Render(name)
}

func (m Model) latencyBadge() string {
	name := m.selectedName()
	d, ok := m.latency[name]
	if name == "" || !ok {
		return m.theme.dim.Render("press t to test")
	}
	if d.Ms == 0 {
		return m.theme.bad.Render("failed" + errSuffix(d.Err))
	}
	text := humanMillis(d.Ms)
	switch {
	case d.Ms < 200:
		return m.theme.ok.Render("● " + text)
	case d.Ms < 600:
		return m.theme.warn.Render("● " + text)
	default:
		return m.theme.bad.Render("● " + text)
	}
}

func errSuffix(err string) string {
	if err == "" {
		return ""
	}
	return " (" + truncate(err, 40) + ")"
}

func (m Model) runDirLabel() string {
	if m.opts.RunDir != "" {
		return m.opts.RunDir
	}
	return "the platform run dir"
}

func pidText(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return fmt.Sprint(pid)
}

func uptimeText(st control.StatusData) string {
	if !st.EngineRunning || st.UptimeSecs <= 0 {
		return "-"
	}
	return humanDuration(st.UptimeSecs)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m Model) shortVersion(v string) string {
	if v == "" {
		return "?"
	}
	return truncate(v, 20)
}

// --- nodes ------------------------------------------------------------------

func (m Model) nodesView() string {
	w := m.panelWidth()
	title := fmt.Sprintf("servers  %d", len(m.servers))
	head := " " + m.theme.title.Render(title) + "   " +
		m.theme.subtle.Render("↑/↓ move · enter select · t latency · r reload")
	if len(m.servers) == 0 {
		lines := []string{
			m.theme.warn.Render("No servers configured."),
			"",
			"  " + m.theme.subtle.Render("add one with") + "     " + m.theme.value.Render("vender import '<share-link>'"),
			"  " + m.theme.subtle.Render("servers live in") + " " + m.theme.value.Render(m.snap.ServersPath),
			"  " + m.theme.subtle.Render("then press") + "       " + m.theme.value.Render("r") + m.theme.subtle.Render(" to reload"),
		}
		if m.snap.Err != "" {
			lines = append(lines, "", m.theme.bad.Render(truncate(m.snap.Err, max(20, w-4))))
		}
		return head + "\n" + m.theme.panel("configuration", w, lines...)
	}
	return head + "\n" + m.theme.panel("servers.yaml", w, strings.Split(m.nodes.View(), "\n")...)
}

// newNodesTable builds the Nodes table. Selection is rendered by vender's own
// marker inside the first column, so the table's own "selected" highlight is
// disabled: it would otherwise be invisible in a no-colour terminal and would
// make the golden views depend on colour detection.
func newNodesTable(th theme) table.Model {
	style := table.DefaultStyles()
	style.Header = th.title
	style.Cell = lipgloss.NewStyle()
	style.Selected = lipgloss.NewStyle()
	return table.New(
		table.WithColumns(nodeColumns(90)),
		table.WithFocused(true),
		table.WithHeight(8),
		table.WithStyles(style),
	)
}

// nodeColumns lays the Nodes table out to exactly the width available inside
// its panel, so the panel border never cuts a column in half. The name column
// absorbs whatever is left over.
func nodeColumns(inner int) []table.Column {
	marker, typ, active, latency := 2, 10, 7, 12
	protocol := clampInt(inner/5, 8, 20)
	endpoint := clampInt(inner/4, 10, 24)
	name := inner - (marker + typ + protocol + endpoint + active + latency)
	if name < 8 {
		name = 8
	}
	return []table.Column{
		{Title: "", Width: marker},
		{Title: "NAME", Width: name},
		{Title: "TYPE", Width: typ},
		{Title: "PROTOCOL", Width: protocol},
		{Title: "ENDPOINT", Width: endpoint},
		{Title: "ACTIVE", Width: active},
		{Title: "LATENCY", Width: latency},
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// nodeTableRows converts the model's rows into table rows. The cursor marker is
// part of the data rather than a style, so it survives a colourless terminal.
func (m Model) nodeTableRows() []table.Row {
	rows := make([]table.Row, 0, len(m.servers))
	cursor := m.nodes.Cursor()
	for i, s := range m.servers {
		mark := "  "
		if i == cursor {
			mark = "▸ "
		}
		active := ""
		if s.Active {
			active = "●"
		}
		rows = append(rows, table.Row{mark, s.Name, s.Type, s.Protocol, s.Endpoint, active, s.Latency})
	}
	return rows
}

// --- logs -------------------------------------------------------------------

func (m Model) logsView() string {
	w := m.panelWidth()
	lines := []string{}
	if m.view.Width > 0 {
		lines = strings.Split(m.view.View(), "\n")
	}
	body := m.theme.panel("sing-box log", w, lines...)

	follow := m.theme.dim.Render("follow: off")
	if m.logFollow {
		follow = m.theme.ok.Render("follow: on")
	}
	filter := m.theme.dim.Render("filter: (none)")
	if m.logFilter != "" {
		filter = m.theme.value.Render("filter: " + m.logFilter)
	}
	status := " " + follow + "   " + filter + "   " +
		m.theme.subtle.Render(fmt.Sprintf("%d lines buffered", len(m.logs))) +
		"   " + m.theme.dim.Render("f follow · / filter · t clear · alt+screen scrolls")
	if m.logInput {
		status = " " + m.theme.accent.Render("filter> ") + m.theme.value.Render(m.logInputBuf) + m.theme.accent.Render("▌") +
			m.theme.dim.Render("   enter apply · esc cancel")
	}
	if !m.attached {
		status = " " + m.theme.bad.Render("○ daemon not running") + m.theme.subtle.Render("  — logs stream from the daemon's engine")
	}
	return body + "\n" + status
}

// logLineView renders one engine line with its timestamp and source.
func (m Model) logLineView(l control.LogLine) string {
	stamp := ""
	if l.Time != "" {
		stamp = m.theme.dim.Render(l.Time) + " "
	}
	body := l.Line
	if l.Source == control.SourceDaemon {
		body = m.theme.accent.Render("vender") + " " + m.theme.subtle.Render(body)
	} else {
		body = m.logLevelStyle(l.Line)
	}
	// Highlight the filter term so the match is visible in context.
	if m.logFilter != "" {
		body = highlight(body, l.Line, m.logFilter, m.theme)
	}
	return stamp + body
}

func (m Model) logLevelStyle(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "ERROR") || strings.Contains(upper, "FATAL"):
		return m.theme.bad.Render(line)
	case strings.Contains(upper, "WARN"):
		return m.theme.warn.Render(line)
	case strings.Contains(upper, "DEBUG"):
		return m.theme.dim.Render(line)
	default:
		return m.theme.value.Render(line)
	}
}

// highlight underlines the filter term inside an already-styled line. When the
// line carries no escapes (the common case) this is a straight substring
// search; when it does, the styled line is returned unchanged rather than
// risking a broken escape sequence.
func highlight(styled, plain, term string, th theme) string {
	if strings.ContainsRune(styled, 0x1b) {
		return styled
	}
	idx := strings.Index(strings.ToLower(plain), strings.ToLower(term))
	if idx < 0 {
		return styled
	}
	return plain[:idx] + th.accent.Underline(true).Render(plain[idx:idx+len(term)]) + plain[idx+len(term):]
}

// --- settings ---------------------------------------------------------------

func (m Model) settingsView() string {
	w := m.panelWidth()
	mode := m.currentMode()

	modeLines := make([]string, 0, len(config.ProxyModes())+1)
	for i, pm := range config.ProxyModes() {
		mark := "( )"
		if string(pm) == mode {
			mark = "(●)"
		}
		cursor := "  "
		if i == m.settingsCursor {
			cursor = "▸ "
		}
		line := m.theme.value.Render(cursor + mark + " " + pad(string(pm), 7))
		if string(pm) == mode {
			line += m.theme.ok.Render("active  ")
		} else {
			line += m.theme.dim.Render("        ")
		}
		modeLines = append(modeLines, line+m.theme.subtle.Render(modeDescription(pm)))
	}
	modeLines = append(modeLines, m.theme.dim.Render("←/→ choose · enter apply · applying restarts the engine in place"))

	listener := []string{
		m.theme.subtle.Render("mixed SOCKS5+HTTP  ") + m.theme.value.Render(orDash(m.listenLabel())),
		m.theme.subtle.Render("log level          ") + m.theme.value.Render(orDash(m.snap.LogLevel)) +
			m.theme.subtle.Render("   bind interface  ") + m.theme.value.Render(orDash(m.snap.BindInterface)),
		m.theme.subtle.Render("engine binary      ") + m.theme.value.Render(orDash(m.snap.Binary)),
		m.theme.subtle.Render("config dir         ") + m.theme.value.Render(m.snap.ConfigDir),
	}

	policy := m.policyLines(mode)

	autostart := []string{
		m.theme.subtle.Render("start at login  ") + m.theme.dim.Render("not implemented in this milestone (M4)") +
			m.theme.subtle.Render("      system proxy  ") + m.theme.dim.Render("`vender proxy on|off` (Windows: M4)"),
	}

	return strings.Join([]string{
		m.theme.panel("routing mode", w, modeLines...),
		m.theme.panel("listener", w, listener...),
		m.theme.panel("built-in routing policy", w, policy...),
		m.theme.panel("autostart", w, autostart...),
	}, "\n")
}

func (m Model) listenLabel() string {
	if m.attached && m.status.Listen != "" {
		return m.status.Listen
	}
	return m.snap.Listen
}

func modeDescription(pm config.ProxyMode) string {
	switch pm {
	case config.ModeRule:
		return "built-in policy: CN and private destinations direct, ads blocked, the rest proxied"
	case config.ModeGlobal:
		return "every connection through the selected server"
	default:
		return "no proxy at all (the local inbound still works)"
	}
}

// policyLines renders the built-in routing policy from internal/rules, so the
// Settings screen is informative even with no daemon running.
func (m Model) policyLines(mode string) []string {
	if mode != string(config.ModeRule) {
		if mode == string(config.ModeDirect) {
			return []string{m.theme.dim.Render("the built-in policy applies in `rule` mode only; direct mode uses no proxy")}
		}
		return []string{m.theme.dim.Render("global mode sends everything through the proxy, so no routing rules are applied")}
	}
	lines := []string{
		m.theme.subtle.Render("actions  ") + m.theme.value.Render(joinSummaries(rules.BuiltinActions())) +
			m.theme.subtle.Render("      final  ") + m.theme.accent.Render(string(rules.OutProxy)),
	}
	for i, r := range rules.BuiltinRules() {
		lines = append(lines, fmt.Sprintf("%2d  %s  %s  %s",
			i+1,
			m.theme.value.Render(pad(r.Name, 22)),
			m.theme.subtle.Render(pad(r.Summary(), 46)),
			m.theme.accent.Render(string(r.Outbound))))
	}
	d := rules.BuiltinDNS()
	lines = append(lines,
		m.theme.subtle.Render("dns      ")+m.theme.value.Render(fmt.Sprintf("%d servers, %s, final %s", len(d.Servers), plural(len(d.Rules), "rule"), d.Final))+
			m.theme.subtle.Render("      sets  ")+m.theme.value.Render(fmt.Sprintf("%d embedded, %s", len(rules.ReferencedRuleSets()), humanBytes(float64(totalRuleSetSize())))),
		m.theme.dim.Render(fmt.Sprintf("source   %s @ %s (2026-10-15)", rules.RuleSetSourceRepo, shortCommit(rules.RuleSetSourceCommit))),
	)
	return lines
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func joinSummaries(actions []rules.Action) string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Summary())
	}
	return strings.Join(out, ", ")
}

func dnsSummary() string {
	d := rules.BuiltinDNS()
	return fmt.Sprintf("%d servers, %d rules, final %s", len(d.Servers), len(d.Rules), d.Final)
}

func totalRuleSetSize() int {
	total := 0
	for _, rs := range rules.RuleSets() {
		if n, err := rules.RuleSetSize(rs.Tag); err == nil {
			total += n
		}
	}
	return total
}

func shortCommit(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}

// --- help -------------------------------------------------------------------

func (m Model) helpOverlay() string {
	w := m.panelWidth()
	groups := [][]string{
		{
			"1 / 2 / 3 / 4    Dashboard, Nodes, Logs, Settings",
			"tab / shift+tab  next / previous screen",
			"?                this help",
			"q / ctrl+c       quit",
		},
		{
			"Dashboard",
			"  r    refresh the daemon status (or retry attaching)",
			"  t    test the active server's latency",
			"  s    start the engine      x  stop it      R  restart it",
			"  m    cycle the routing mode",
		},
		{
			"Nodes",
			"  ↑/↓, j/k   move      enter  select (restarts the engine)",
			"  t          latency for the highlighted server",
			"  r          reload servers.yaml from disk",
		},
		{
			"Logs",
			"  f          toggle follow      /  filter      t  clear the filter",
			"  ↑/↓, pgup/pgdn, g/G         scroll",
		},
		{
			"Settings",
			"  ←/→, h/l   choose a mode     enter  apply",
		},
		{
			"vender is a client: the daemon owns the engine, and every action here goes",
			"through its local control channel. The TUI works with no daemon running.",
		},
	}

	var lines []string
	for i, g := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		for j, l := range g {
			if j == 0 && len(g) > 1 {
				lines = append(lines, m.theme.title.Render(l))
				continue
			}
			lines = append(lines, "  "+m.theme.value.Render(l))
		}
	}
	return m.theme.panel("help — press ? to close", w, lines...)
}

// keyMap describes the current screen's shortcuts. The short form feeds the
// footer; key.Binding is used so bubbles' help component can lay it out.
func (m Model) keyMap() help.KeyMap {
	b := func(k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(k), key.WithHelp(k, desc))
	}
	common := []key.Binding{b("tab", "screens"), b("?", "help")}
	switch m.screen {
	case screenNodes:
		return km{short: append([]key.Binding{
			b("↑/↓", "move"), b("enter", "select"), b("t", "latency"), b("r", "reload"),
		}, common...)}
	case screenLogs:
		var f key.Binding
		if m.logFollow {
			f = b("f", "follow off")
		} else {
			f = b("f", "follow on")
		}
		return km{short: append([]key.Binding{f, b("/", "filter"), b("t", "clear")}, common...)}
	case screenSettings:
		return km{short: append([]key.Binding{
			b("←/→", "mode"), b("enter", "apply"),
		}, common...)}
	default:
		short := []key.Binding{b("t", "latency"), b("s/x", "start/stop"), b("m", "mode")}
		if !m.attached {
			short = []key.Binding{b("r", "retry attach")}
		}
		return km{short: append(short, common...)}
	}
}

// km is a minimal help.KeyMap with only a short form.
type km struct{ short []key.Binding }

func (k km) ShortHelp() []key.Binding { return k.short }
func (k km) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.short}
}
