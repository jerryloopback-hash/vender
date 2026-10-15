package tui

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ll-lite/vender/internal/autostart"
	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
)

var update = flag.Bool("update", false, "rewrite the golden view files in testdata/")

// Dummy values only: never commit real credentials.
const (
	testUUID = "11111111-1111-1111-1111-111111111111"
	testPBK  = "DUMMY-FAKE-PUBLIC-KEY-FOR-TESTS-ONLY-0000000000"
	testSID  = "0123456789abcdef"
	testPW   = "22222222-3333-4444-5555-666666666666"
)

// The view tests build a model by hand and assert on View(). No terminal, no
// daemon and no goroutine is involved: View is pure, so the same state always
// renders the same text. That is the whole point of keeping the model free of
// I/O.
const (
	testWidth  = 100
	testHeight = 30
)

// newTestModel returns a model sized like a normal terminal, with the plain
// (no-colour) theme so the golden text is what a NO_COLOR user sees.
func newTestModel(t *testing.T) Model {
	t.Helper()
	m := newModel(context.Background(), Options{
		ConfigDir: "/home/test/.config/vender",
		RunDir:    "/home/test/.cache/vender",
	}, newTheme(nil))
	m.resize(testWidth, testHeight)
	return m
}

// fixtureServers is a plausible two-server configuration: one REALITY node and
// one Hysteria2 node, with names that exercise column widths.
func fixtureServers() []config.Server {
	return []config.Server{
		{
			Name: "Interserver-LA", Type: config.TypeVLESS, Address: "162.35.170.235", Port: 443,
			UUID: testUUID, Security: config.SecurityReality, SNI: "www.apple.com",
			PublicKey: testPBK, ShortID: testSID, Fingerprint: "chrome", Flow: "xtls-rprx-vision",
		},
		{
			Name: "Interserver-LA-Hy2", Type: config.TypeHysteria2, Address: "162.35.170.235", Port: 8443,
			Password: testPW, SNI: "www.bing.com", Obfs: "salamander", ObfsPassword: "cafebabe", Insecure: true,
		},
	}
}

func fixtureSnapshot(servers []config.Server) configSnapshot {
	return configSnapshot{
		ConfigDir:   "/home/test/.config/vender",
		ServersPath: "/home/test/.config/vender/servers.yaml",
		Listen:      "127.0.0.1:7897",
		Mode:        "rule",
		LogLevel:    "info",
		Binary:      "/usr/local/bin/sing-box",
		Selected:    "Interserver-LA",
		Servers:     servers,
	}
}

// runningModel is the Dashboard with a healthy daemon and some traffic.
func runningModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.attached = true
	m.status = control.StatusData{
		Vender: "1.2.3", PID: 4821, RunDir: "/home/test/.cache/vender",
		Listen: "127.0.0.1:7897", Mode: "rule", Selected: "Interserver-LA",
		SrvType: "vless", SrvEndpoint: "162.35.170.235:443", SrvDetail: "reality/xtls-rprx-vision tcp sni=www.apple.com",
		EngineRunning: true, EnginePid: 4829, EngineHealthy: true,
		EngineVersion: "sing-box 1.14.3", UptimeSecs: 192,
		ClashListen: "127.0.0.1:41234", ServerCount: 2,
	}
	m.snap = fixtureSnapshot(fixtureServers())
	m.selected = "Interserver-LA"
	m.latency["Interserver-LA"] = control.LatencyData{Name: "Interserver-LA", Ms: 924}
	// A fixed, deterministic traffic history: a burst that decays. The rate
	// values are in bytes per second (KiB steps here), and the totals are the
	// whole-session counters the engine reports.
	peaks := []float64{900, 2400, 5200, 9800, 6400, 3100, 1800, 1200, 980, 760, 640, 520}
	for _, v := range peaks {
		m.pushTraffic(control.TrafficSample{
			Down: v * 1024, Up: v * 96,
			TotalDown: 12 * 1024 * 1024, TotalUp: 1536 * 1024,
		})
	}
	m.rebuildRows()
	return m
}

// detachedModel is the Dashboard with no daemon: the state the TUI starts in
// on a fresh machine, and the one that must never look like a crash.
func detachedModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.snap = fixtureSnapshot(fixtureServers())
	m.attachErr = "control: no daemon is running in /home/test/.cache/vender: open /home/test/.cache/vender/control.json: no such file or directory"
	m.rebuildRows()
	return m
}

func TestViewDashboardRunning(t *testing.T) {
	assertGolden(t, "dashboard_running.golden", runningModel(t).View(), []string{
		"daemon running",
		"engine running",
		"Interserver-LA",
		"127.0.0.1:7897",
		"924 ms",
		"520 KiB/s",
		"12.00 MiB",
	})
}

func TestViewDashboardDaemonDown(t *testing.T) {
	view := detachedModel(t).View()
	assertGolden(t, "dashboard_detached.golden", view, []string{
		"daemon not running",
		"vender daemon",
		"/home/test/.cache/vender",
		"no live traffic",
	})
	if strings.Contains(view, "panic") {
		t.Errorf("the detached dashboard looks like a crash:\n%s", view)
	}
}

func TestViewNodesPopulated(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenNodes)
	m.tabAnim = tabAnimSteps
	m.latency["Interserver-LA-Hy2"] = control.LatencyData{Name: "Interserver-LA-Hy2", Ms: 38, Err: "tcp only (not the active server)"}
	m.rebuildRows()

	assertGolden(t, "nodes_populated.golden", m.View(), []string{
		"Interserver-LA", "Interserver-LA-Hy2",
		"vless", "hysteria2",
		"162.35.170.235:443", "162.35.170.235:8443",
		"924 ms", "38 ms (tcp)",
		"▸",
	})
}

func TestViewNodesEmpty(t *testing.T) {
	m := newTestModel(t)
	m.snap = fixtureSnapshot(nil)
	m = m.switchScreen(screenNodes)
	m.tabAnim = tabAnimSteps
	m.rebuildRows()

	assertGolden(t, "nodes_empty.golden", m.View(), []string{
		"No servers configured",
		"vender import",
		"/home/test/.config/vender/servers.yaml",
	})
}

func TestViewSettings(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenSettings)
	m.tabAnim = tabAnimSteps

	assertGolden(t, "settings.golden", m.View(), []string{
		"routing mode", "rule", "global", "direct",
		"127.0.0.1:7897",
		"built-in routing policy",
		"private-dest-direct", "ads-block", "cn-direct",
		"final", "dns", "embedded,",
		"start at login", "checking",
	})
}

// The autostart panel is built from state the TUI reads itself, so it is
// complete with no daemon running and shows the concrete artifact.
func TestSettingsShowsTheAutostartState(t *testing.T) {
	m := detachedModel(t)
	m = m.switchScreen(screenSettings)
	m.autostartKnown = true
	m.startAtLogin = autostart.State{
		Enabled:   true,
		Mechanism: autostart.MechanismSystemdUser,
		Target:    "/home/test/.config/systemd/user/vender.service",
		Detail:    "lingering: on",
	}
	view := m.View()
	for _, want := range []string{"enabled", "systemd-user", "/home/test/.config/systemd/user/vender.service", "press a to disable"} {
		if !strings.Contains(view, want) {
			t.Errorf("settings does not show %q:\n%s", want, view)
		}
	}
}

// `a` on the Settings screen toggles start-at-login through a command (so the
// file/registry I/O never blocks the UI) and marks the screen busy while it
// runs. The command itself is not executed here: it would write to the host.
func TestSettingsToggleAutostartIsACommand(t *testing.T) {
	m := newTestModel(t)
	m = m.switchScreen(screenSettings)

	next, cmd := m.handleSettingsKey("a")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("a did not issue an autostart command")
	}
	if !strings.Contains(m.busy, "autostart") {
		t.Errorf("busy = %q", m.busy)
	}
}

// An autostart reply updates the panel and reports the result.
func TestAutostartReplyUpdatesTheModel(t *testing.T) {
	m := newTestModel(t)
	m = m.switchScreen(screenSettings)
	next, _ := m.Update(autostartMsg{
		state: autostart.State{Enabled: true, Mechanism: autostart.MechanismSystemdUser},
		note:  "autostart on",
	})
	m = next.(Model)
	if !m.autostartKnown || !m.startAtLogin.Enabled {
		t.Errorf("state after a reply = %+v", m.startAtLogin)
	}
	if !strings.Contains(m.toast, "enabled") {
		t.Errorf("toast = %q", m.toast)
	}

	next, _ = m.Update(autostartMsg{err: errors.New("no polkit")})
	m = next.(Model)
	if m.autostartErr == "" {
		t.Error("a failed probe was not recorded")
	}
}

func TestViewSettingsWithoutADaemonStillShowsPolicyAndDaemonDown(t *testing.T) {
	m := detachedModel(t)
	m = m.switchScreen(screenSettings)
	m.tabAnim = tabAnimSteps
	view := m.View()
	for _, want := range []string{"built-in routing policy", "private-dest-direct", "daemon not running"} {
		if !strings.Contains(view, want) {
			t.Errorf("settings without a daemon does not contain %q:\n%s", want, view)
		}
	}
}

func TestViewLogs(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenLogs)
	m.tabAnim = tabAnimSteps
	m.appendLog(control.LogLine{Time: "19:00:24", Source: control.SourceEngine, Line: "INFO inbound/mixed[mixed-in]: tcp server started at 127.0.0.1:7897"})
	m.appendLog(control.LogLine{Time: "19:00:25", Source: control.SourceEngine, Line: "INFO [1] outbound/vless[proxy]: outbound connection to www.example.com:443"})
	m.appendLog(control.LogLine{Time: "19:00:26", Source: control.SourceEngine, Line: "WARN [2] connection upload: write: broken pipe"})
	m.appendLog(control.LogLine{Time: "19:00:27", Source: control.SourceDaemon, Line: "engine stopped"})

	view := m.View()
	for _, want := range []string{"follow: on", "19:00:24", "outbound connection", "vender engine stopped", "4 lines buffered"} {
		if !strings.Contains(view, want) {
			t.Errorf("logs view does not contain %q:\n%s", want, view)
		}
	}
}

func TestViewLogsFilter(t *testing.T) {
	m := runningModel(t)
	m.screen = screenLogs
	m.logFollow = true
	m.appendLog(control.LogLine{Line: "INFO keep me"})
	m.appendLog(control.LogLine{Line: "DEBUG skip me"})
	m.logFilter = "keep"
	m.syncViewport()

	view := m.View()
	if !strings.Contains(view, "keep me") || strings.Contains(view, "skip me") {
		t.Errorf("filter did not apply:\n%s", view)
	}
	if !strings.Contains(view, "filter: keep") {
		t.Errorf("the filter is not shown in the status line:\n%s", view)
	}
}

func TestViewHelpOverlay(t *testing.T) {
	m := runningModel(t)
	m.showHelp = true
	view := m.View()
	for _, want := range []string{"help", "quit", "Dashboard", "Nodes", "Logs", "Settings"} {
		if !strings.Contains(view, want) {
			t.Errorf("help overlay does not mention %q:\n%s", want, view)
		}
	}
}

func TestViewBusyShowsASpinner(t *testing.T) {
	m := runningModel(t)
	m.busy = "switching to Interserver-LA-Hy2…"
	view := m.View()
	if !strings.Contains(view, "switching to Interserver-LA-Hy2") {
		t.Errorf("the busy label is missing:\n%s", view)
	}
	if !strings.Contains(view, m.spinner.Spinner.Frames[0]) {
		t.Errorf("the spinner frame is missing:\n%s", view)
	}
}

func TestViewTinyTerminalDoesNotPanic(t *testing.T) {
	for _, size := range [][2]int{{20, 5}, {40, 8}, {200, 60}, {10, 3}} {
		m := runningModel(t)
		m.resize(size[0], size[1])
		for _, s := range []screen{screenDashboard, screenNodes, screenLogs, screenSettings} {
			m = m.switchScreen(s)
			if got := m.View(); got == "" {
				t.Errorf("size %v screen %v rendered nothing", size, s)
			}
		}
	}
}

// --- behaviour --------------------------------------------------------------

func TestGlobalKeysSwitchScreensAndQuit(t *testing.T) {
	m := newTestModel(t)

	next, cmd := m.handleKey(keyMsg("tab"))
	m = next.(Model)
	if m.screen != screenNodes || m.fromTab != 0 {
		t.Errorf("tab: screen = %v, from = %d", m.screen, m.fromTab)
	}
	if cmd != nil {
		t.Errorf("switching screens should not return a command, got %v", cmd)
	}

	next, _ = m.handleKey(keyMsg("4"))
	m = next.(Model)
	if m.screen != screenSettings {
		t.Errorf("4: screen = %v, want Settings", m.screen)
	}

	next, _ = m.handleKey(keyMsg("shift+tab"))
	m = next.(Model)
	if m.screen != screenLogs {
		t.Errorf("shift+tab: screen = %v, want Logs", m.screen)
	}

	_, cmd = m.handleKey(keyMsg("q"))
	if cmd == nil {
		t.Error("q did not request a quit")
	}

	next, _ = m.handleKey(keyMsg("?"))
	m = next.(Model)
	if !m.showHelp {
		t.Error("? did not open the help overlay")
	}
	next, _ = m.handleKey(keyMsg("esc"))
	m = next.(Model)
	if m.showHelp {
		t.Error("esc did not close the help overlay")
	}
}

// With no daemon, every action that needs one must explain itself instead of
// doing nothing or crashing.
func TestActionsWithoutADaemonExplainThemselves(t *testing.T) {
	m := detachedModel(t)

	next, cmd := m.handleDashboardKey("s")
	m = next.(Model)
	if cmd != nil {
		t.Error("starting the engine with no daemon returned a command")
	}
	if !strings.Contains(m.toast, "vender daemon") || !m.toastErr {
		t.Errorf("toast = %q (err=%v), want a hint about `vender daemon`", m.toast, m.toastErr)
	}

	next, cmd = m.handleSettingsKey("enter")
	m = next.(Model)
	if cmd != nil {
		t.Error("applying a mode with no daemon returned a command")
	}

	m2 := detachedModel(t)
	m2 = m2.switchScreen(screenNodes)
	m2.rebuildRows()
	// Move off the active server first: selecting the active one is a no-op,
	// which is deliberate and tested separately.
	next, _ = m2.handleNodesKey(keyMsg("down"))
	m2 = next.(Model)
	next, cmd = m2.handleNodesKey(keyMsg("enter"))
	m = next.(Model)
	if cmd != nil {
		t.Error("selecting a server with no daemon returned a command")
	}
	if !strings.Contains(m.toast, "vender daemon") {
		t.Errorf("toast = %q", m.toast)
	}
}

// Selecting a server must go to the daemon (the only component that can
// restart the engine) and must mark the UI busy while it waits.
func TestSelectingAServerCallsTheDaemon(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenNodes)
	m.tabAnim = tabAnimSteps
	m.rebuildRows()

	next, cmd := m.handleNodesKey(keyMsg("down"))
	m = next.(Model)
	if m.nodes.Cursor() != 1 {
		t.Fatalf("cursor = %d, want 1", m.nodes.Cursor())
	}

	next, cmd = m.handleNodesKey(keyMsg("enter"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("enter did not issue a select command")
	}
	if !strings.Contains(m.busy, "Interserver-LA-Hy2") {
		t.Errorf("busy = %q", m.busy)
	}

	// A no-op selection of the active server must not drop the connection.
	m2 := runningModel(t)
	m2 = m2.switchScreen(screenNodes)
	m2.rebuildRows()
	next, cmd = m2.handleNodesKey(keyMsg("enter"))
	m2 = next.(Model)
	if cmd != nil {
		t.Error("selecting the already-active server issued a command")
	}
	if !strings.Contains(m2.toast, "already active") {
		t.Errorf("toast = %q", m2.toast)
	}
}

func TestModeCyclingWrapsAndApplies(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenSettings)

	// The cursor starts on the mode the daemon reports, so applying it is a
	// deliberate no-op: a mode change restarts the engine, and there is no
	// reason to drop the connection for nothing.
	next, cmd := m.handleSettingsKey("enter")
	m = next.(Model)
	if cmd != nil {
		t.Error("applying the current mode issued a command")
	}
	if !strings.Contains(m.toast, "already in rule") {
		t.Errorf("toast = %q", m.toast)
	}

	// Move to global and apply it.
	next, _ = m.handleSettingsKey("l")
	m = next.(Model)
	if m.settingsCursor != indexOfMode(config.ModeGlobal) {
		t.Fatalf("cursor = %d, want global", m.settingsCursor)
	}
	next, cmd = m.handleSettingsKey("enter")
	m = next.(Model)
	if cmd == nil || !strings.Contains(m.busy, "global") {
		t.Fatalf("enter on global: busy = %q, cmd = %v", m.busy, cmd)
	}

	// 'm' cycles the mode without moving the cursor: global -> direct.
	m.busy = ""
	m.status.Mode = "global"
	m.settingsCursor = indexOfMode(config.ModeGlobal)
	next, cmd = m.handleSettingsKey("m")
	m = next.(Model)
	if cmd == nil || !strings.Contains(m.busy, "direct") {
		t.Fatalf("m: busy = %q, cmd = %v", m.busy, cmd)
	}
}

func TestLogFilterEditing(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenLogs)

	next, _ := m.handleKey(keyMsg("/"))
	m = next.(Model)
	if !m.logInput {
		t.Fatal("/ did not open the filter editor")
	}
	for _, r := range "proxy" {
		next, _ = m.handleKey(keyMsg(string(r)))
		m = next.(Model)
	}
	if m.logInputBuf != "proxy" {
		t.Fatalf("buffer = %q", m.logInputBuf)
	}
	next, _ = m.handleKey(keyMsg("enter"))
	m = next.(Model)
	if m.logInput || m.logFilter != "proxy" {
		t.Errorf("filter = %q (editing=%v)", m.logFilter, m.logInput)
	}

	// While the editor is open, "q" must be a filter character, not a quit.
	m.logInput = true
	m.logInputBuf = ""
	next, cmd := m.handleKey(keyMsg("q"))
	m = next.(Model)
	if cmd != nil {
		t.Error("q quit while the filter editor was open")
	}
	if m.logInputBuf != "q" {
		t.Errorf("buffer = %q, want q", m.logInputBuf)
	}
}

// A daemon that stops answering must not make the TUI queue up status
// requests: one poll is in flight at a time.
func TestStatusPollsDoNotStackUp(t *testing.T) {
	m := runningModel(t)

	next, cmd := m.Update(tickMsg{})
	m = next.(Model)
	if cmd == nil || !m.polling {
		t.Fatalf("the first tick did not start a poll (polling=%v)", m.polling)
	}
	// A second tick while the first is still in flight must not add another
	// poll (the returned batch still carries the tick and the animation).
	next, cmd = m.Update(tickMsg{})
	m = next.(Model)
	if !m.polling {
		t.Error("polling was cleared without a reply")
	}
	if cmd == nil {
		t.Error("the data tick stopped rescheduling itself")
	}
	// A reply clears the flag, so the next tick polls again.
	next, _ = m.Update(statusMsg{st: m.status})
	m = next.(Model)
	if m.polling {
		t.Error("a status reply did not clear the in-flight flag")
	}
	if next, cmd = m.Update(tickMsg{}); cmd == nil || !next.(Model).polling {
		t.Error("polling did not resume after a reply")
	}
}

func TestTrafficSamplesAreBoundedAndTotalsTracked(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < sampleWindow+25; i++ {
		m.pushTraffic(control.TrafficSample{Down: float64(i), Up: float64(i) / 2, TotalDown: int64(i * 10), TotalUp: int64(i)})
	}
	if len(m.downRates) != sampleWindow || len(m.upRates) != sampleWindow {
		t.Errorf("windows = %d/%d, want %d", len(m.downRates), len(m.upRates), sampleWindow)
	}
	if got := m.currentDown(); got != float64(sampleWindow+24) {
		t.Errorf("newest sample = %v", got)
	}
	if m.totalDown != int64((sampleWindow+24)*10) {
		t.Errorf("total down = %d", m.totalDown)
	}
}

func TestDetachStopsStreamsAndKeepsTheScreenUsable(t *testing.T) {
	m := runningModel(t)
	m.detach(errors.New("connection reset"))
	if m.attached || m.cli != nil {
		t.Error("detach left the client attached")
	}
	if m.attachErr == "" {
		t.Error("detach did not record why")
	}
	if view := m.View(); !strings.Contains(view, "daemon not running") {
		t.Errorf("the dashboard does not reflect the detach:\n%s", view)
	}
	// The next data tick must try to attach again, so a daemon started later
	// is picked up without restarting the TUI.
	next, cmd := m.Update(tickMsg{})
	m = next.(Model)
	if !m.attaching || cmd == nil {
		t.Errorf("a tick did not schedule a re-attach (attaching=%v, cmd=%v)", m.attaching, cmd)
	}
}

// --- helpers ----------------------------------------------------------------

// keyMsg builds the KeyMsg a terminal would send for a named key or a rune, so
// the tests exercise the same dispatch path as the real program.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func assertGolden(t *testing.T, name, got string, wants []string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("view does not contain %q", w)
		}
	}
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run `go test ./internal/tui -update`): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("view differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
