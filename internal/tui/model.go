package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
)

// screen identifies one of the TUI's four screens.
type screen int

const (
	screenDashboard screen = iota
	screenNodes
	screenLogs
	screenSettings
)

var screenNames = []string{"Dashboard", "Nodes", "Logs", "Settings"}

// Layout constants. The tab bar is a row of fixed-width cells, which is what
// makes the sliding highlight a simple column computation.
const (
	tabCellWidth = 12
	tabAnimSteps = 8
	// sampleWindow is how many one-second traffic samples the graph keeps.
	sampleWindow = 90
	// logBuffer caps how much engine output the Logs screen retains.
	logBuffer = 2000
	// toastTicks is how long a status message stays on screen, in refresh
	// intervals (i.e. seconds).
	toastTicks = 5
)

// defaultRefreshInterval is the traffic/status poll cadence when Options leaves
// it unset.
const defaultRefreshInterval = time.Second

// --- messages ---------------------------------------------------------------

type (
	// animMsg drives the animation frames (spinner, tab slide). It runs much
	// faster than the data tick so motion is smooth even though traffic
	// arrives once a second.
	animMsg time.Time
	// tickMsg is the data cadence: poll status, retry attach, expire toasts.
	tickMsg time.Time

	daemonUpMsg struct {
		cli *control.Client
		st  control.StatusData
	}
	daemonDownMsg   struct{ err error }
	statusMsg       struct{ st control.StatusData }
	statusFailedMsg struct{ err error }
	streamReadyMsg  struct {
		name   string
		stream *control.Stream
	}
	streamFailedMsg struct {
		name string
		err  error
	}
	streamEndMsg struct {
		name string
		err  error
	}
	logLineMsg struct{ line control.LogLine }
	trafficMsg struct{ sample control.TrafficSample }
	latencyMsg struct {
		name string
		data control.LatencyData
	}
	actionDoneMsg struct {
		label string
		err   error
	}
	// snapshotMsg is the offline view of the configuration, loaded from disk so
	// the Nodes and Settings screens work with no daemon at all.
	snapshotMsg struct{ snap configSnapshot }
)

// configSnapshot is what the TUI can show about a *stopped* vender: the files
// on disk. It is deliberately separate from control.StatusData, which
// describes a running daemon.
type configSnapshot struct {
	ConfigDir     string
	ServersPath   string
	Listen        string
	Mode          string
	LogLevel      string
	Binary        string
	BindInterface string
	Selected      string
	Servers       []config.Server
	Err           string
}

// --- model ------------------------------------------------------------------

// Model is the whole TUI state. It is deliberately constructible without a
// daemon (and without a terminal), which is what lets the view tests build a
// model by hand and assert on View().
type Model struct {
	ctx   context.Context
	opts  Options
	theme theme

	width, height int
	frame         int64

	screen   screen
	fromTab  int
	tabAnim  int
	showHelp bool

	// daemon connection
	cli       *control.Client
	attached  bool
	attaching bool
	polling   bool
	attachErr string
	status    control.StatusData

	// streams (each owns its own connection; see control.Client)
	logStream  *control.Stream
	trafStream *control.Stream

	// data
	snap      configSnapshot
	servers   []serverRow
	selected  string
	latency   map[string]control.LatencyData
	upRates   []float64
	downRates []float64
	totalUp   int64
	totalDown int64

	// logs
	logs         []control.LogLine
	logFollow    bool
	logFilter    string
	logInput     bool
	logInputBuf  string
	view         viewport.Model
	viewportSync bool

	nodes table.Model

	settingsCursor int

	spinner  spinner.Model
	progress progress.Model
	busy     string
	toast    string
	toastErr bool
	toastTTL int
}

// serverRow is one line of the Nodes table. It is built from the daemon's list
// when attached and from servers.yaml when not, so the screen never depends on
// a daemon to be useful.
type serverRow struct {
	Name     string
	Type     string
	Protocol string
	Endpoint string
	Active   bool
	Latency  string
}

// newModel builds the initial model. It performs no I/O: everything that
// touches the disk or the daemon happens in a tea.Cmd (see Init).
func newModel(ctx context.Context, opts Options, th theme) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	logView := viewport.New(80, 20)
	logView.MouseWheelEnabled = false

	return Model{
		ctx:            ctx,
		opts:           opts,
		theme:          th,
		screen:         screenDashboard,
		latency:        map[string]control.LatencyData{},
		logFollow:      true,
		logs:           make([]control.LogLine, 0, logBuffer),
		view:           logView,
		nodes:          newNodesTable(th),
		spinner:        sp,
		progress:       progress.New(progress.WithWidth(40), progress.WithoutPercentage(), progress.WithSolidFill("63")),
		attachErr:      "",
		settingsCursor: indexOfMode(config.ModeRule),
	}
}

// Init starts the animation loop, the data tick, and the first attach attempt.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		animTick(),
		dataTick(m.interval()),
		m.spinner.Tick,
		loadSnapshotCmd(m.opts),
		attachCmd(m.ctx, m.opts),
	)
}

// interval is the data poll cadence.
func (m Model) interval() time.Duration {
	if m.opts.RefreshInterval <= 0 {
		return defaultRefreshInterval
	}
	return time.Duration(m.opts.RefreshInterval) * time.Second
}

func animTick() tea.Cmd {
	return tea.Tick(time.Second/8, func(t time.Time) tea.Msg { return animMsg(t) })
}

func dataTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// --- commands ---------------------------------------------------------------

// attachCmd dials the daemon and fetches one status snapshot. A successful
// dial plus a successful call is what "attached" means: the endpoint file
// alone is not enough, because it can outlive a crashed daemon by a moment.
func attachCmd(ctx context.Context, opts Options) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cli, err := dial(cctx, opts)
		if err != nil {
			return daemonDownMsg{err: err}
		}
		st, err := cli.Status(cctx)
		if err != nil {
			_ = cli.Close()
			return daemonDownMsg{err: err}
		}
		return daemonUpMsg{cli: cli, st: st}
	}
}

func pollStatusCmd(ctx context.Context, cli *control.Client) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		st, err := cli.Status(cctx)
		if err != nil {
			return statusFailedMsg{err: err}
		}
		return statusMsg{st: st}
	}
}

// streamCmd opens one subscription on its own connection.
func streamCmd(ctx context.Context, opts Options, name string) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cli, err := dial(cctx, opts)
		if err != nil {
			return streamFailedMsg{name: name, err: err}
		}
		var s *control.Stream
		switch name {
		case control.StreamLogs:
			s, err = cli.FollowLogs(cctx)
		case control.StreamTraffic:
			s, err = cli.WatchTraffic(cctx)
		}
		if err != nil {
			_ = cli.Close()
			return streamFailedMsg{name: name, err: err}
		}
		return streamReadyMsg{name: name, stream: s}
	}
}

// readLogCmd waits for one engine log line. Update re-issues it after each
// line, which keeps exactly one read in flight per stream.
func readLogCmd(ctx context.Context, s *control.Stream) tea.Cmd {
	return func() tea.Msg {
		var line control.LogLine
		if err := s.Next(ctx, &line); err != nil {
			return streamEndMsg{name: control.StreamLogs, err: err}
		}
		return logLineMsg{line: line}
	}
}

func readTrafficCmd(ctx context.Context, s *control.Stream) tea.Cmd {
	return func() tea.Msg {
		var sample control.TrafficSample
		if err := s.Next(ctx, &sample); err != nil {
			return streamEndMsg{name: control.StreamTraffic, err: err}
		}
		return trafficMsg{sample: sample}
	}
}

// loadSnapshotCmd reads the configuration files. This is the only reason the
// TUI can be useful with no daemon at all.
func loadSnapshotCmd(opts Options) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg{snap: loadSnapshot(opts)}
	}
}

func loadSnapshot(opts Options) configSnapshot {
	snap := configSnapshot{ConfigDir: opts.ConfigDir}
	store := config.NewStoreAt(opts.ConfigDir)
	snap.ServersPath = store.ServersPath()

	if cfg, err := store.LoadConfig(); err != nil {
		snap.Err = err.Error()
	} else {
		snap.Listen = cfg.Proxy.Listen
		snap.Mode = string(cfg.Proxy.Mode)
		snap.LogLevel = cfg.LogLevel
		snap.Binary = cfg.Engine.Binary
		snap.BindInterface = cfg.Engine.BindInterface
		snap.Selected = cfg.SelectedServer
	}
	if ss, err := store.LoadServers(); err != nil {
		if snap.Err == "" {
			snap.Err = err.Error()
		}
	} else {
		snap.Servers = ss.Servers
	}
	return snap
}

// latencyCmd probes a server. The probe itself happens inside the daemon (it
// is the only component that can reach the engine), so this is a plain call.
//
// Action commands dial their own short-lived connection instead of sharing the
// model's: a probe or a restart can take seconds, and the periodic status poll
// must keep flowing while it does.
func latencyCmd(ctx context.Context, opts Options, name string) tea.Cmd {
	return actionCall(ctx, opts, func(cli *control.Client, cctx context.Context) tea.Msg {
		data, err := cli.Latency(cctx, name)
		if err != nil {
			return latencyMsg{name: name, data: control.LatencyData{Name: name, Err: err.Error()}}
		}
		return latencyMsg{name: name, data: data}
	})
}

func selectServerCmd(ctx context.Context, opts Options, name string) tea.Cmd {
	return actionCall(ctx, opts, func(cli *control.Client, cctx context.Context) tea.Msg {
		applied, err := cli.Select(cctx, name)
		if err != nil {
			return actionDoneMsg{label: "select " + name, err: err}
		}
		return actionDoneMsg{label: "switched to " + applied.Status.Selected}
	})
}

func setModeCmd(ctx context.Context, opts Options, mode string) tea.Cmd {
	return actionCall(ctx, opts, func(cli *control.Client, cctx context.Context) tea.Msg {
		if _, err := cli.SetMode(cctx, mode); err != nil {
			return actionDoneMsg{label: "mode " + mode, err: err}
		}
		return actionDoneMsg{label: "mode set to " + mode}
	})
}

func engineCmd(ctx context.Context, opts Options, action string) tea.Cmd {
	return actionCall(ctx, opts, func(cli *control.Client, cctx context.Context) tea.Msg {
		var err error
		switch action {
		case control.CmdStart:
			_, err = cli.StartEngine(cctx)
		case control.CmdStop:
			_, err = cli.StopEngine(cctx)
		case control.CmdRestart:
			_, err = cli.RestartEngine(cctx)
		}
		if err != nil {
			return actionDoneMsg{label: action, err: err}
		}
		return actionDoneMsg{label: "engine " + action}
	})
}

// actionCall runs fn against a freshly dialled client. Server switches and
// restarts can legitimately take a while (the daemon stops and starts sing-box
// inside the call), so the timeout is generous while the UI keeps animating.
func actionCall(ctx context.Context, opts Options, fn func(*control.Client, context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		cli, err := dial(cctx, opts)
		if err != nil {
			return actionDoneMsg{label: "connect", err: err}
		}
		defer cli.Close()
		return fn(cli, cctx)
	}
}

// --- update -----------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case animMsg:
		m.frame++
		if m.tabAnim < tabAnimSteps {
			m.tabAnim++
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, tea.Batch(cmd, animTick())

	case tickMsg:
		m.frame++
		cmds := []tea.Cmd{dataTick(m.interval())}
		if m.toast != "" {
			if m.toastTTL--; m.toastTTL <= 0 {
				m.toast = ""
			}
		}
		switch {
		case m.attached && !m.polling:
			// One poll in flight at a time: a daemon that stops answering must
			// not make the TUI pile up requests behind it.
			m.polling = true
			cmds = append(cmds, pollStatusCmd(m.ctx, m.cli))
		case m.attached:
		case !m.attaching:
			m.attaching = true
			cmds = append(cmds, attachCmd(m.ctx, m.opts))
		}
		return m, tea.Batch(cmds...)

	case daemonUpMsg:
		m.attaching = false
		m.polling = false
		m.attachErr = ""
		m.attached = true
		m.cli = msg.cli
		m.status = msg.st
		m.syncRowsFromStatus()
		return m, tea.Batch(
			streamCmd(m.ctx, m.opts, control.StreamLogs),
			streamCmd(m.ctx, m.opts, control.StreamTraffic),
		)

	case daemonDownMsg:
		m.attaching = false
		m.polling = false
		m.detach(msg.err)
		return m, nil

	case statusMsg:
		m.polling = false
		wasAttached := m.attached
		m.attached = true
		m.status = msg.st
		m.syncRowsFromStatus()
		if !wasAttached {
			return m, nil
		}
		return m, nil

	case statusFailedMsg:
		m.polling = false
		m.detach(msg.err)
		return m, nil

	case streamReadyMsg:
		switch msg.name {
		case control.StreamLogs:
			m.logStream = msg.stream
			return m, readLogCmd(m.ctx, msg.stream)
		case control.StreamTraffic:
			m.trafStream = msg.stream
			return m, readTrafficCmd(m.ctx, msg.stream)
		}
		return m, nil

	case streamFailedMsg:
		// A stream that cannot be opened is not fatal: the daemon may be
		// restarting. The next tick retries via the attach path.
		m.toast = "cannot follow " + msg.name + ": " + errText(msg.err)
		m.toastErr = true
		m.toastTTL = toastTicks
		return m, nil

	case streamEndMsg:
		switch msg.name {
		case control.StreamLogs:
			closeStream(m.logStream)
			m.logStream = nil
			m.appendLog(control.LogLine{Line: "— log stream closed (" + errText(msg.err) + ") —", Source: control.SourceDaemon})
		case control.StreamTraffic:
			closeStream(m.trafStream)
			m.trafStream = nil
		}
		return m, nil

	case logLineMsg:
		m.appendLog(msg.line)
		var cmd tea.Cmd
		if m.logStream != nil {
			cmd = readLogCmd(m.ctx, m.logStream)
		}
		return m, cmd

	case trafficMsg:
		m.pushTraffic(msg.sample)
		var cmd tea.Cmd
		if m.trafStream != nil {
			cmd = readTrafficCmd(m.ctx, m.trafStream)
		}
		return m, cmd

	case latencyMsg:
		m.busy = ""
		// Key the result by the server the daemon actually probed: an empty
		// request name means "the active server", and the badge looks it up by
		// name.
		key := msg.data.Name
		if key == "" {
			key = msg.name
		}
		m.latency[key] = msg.data
		if msg.data.Ms == 0 {
			reason := msg.data.Err
			if reason == "" {
				reason = "no answer"
			}
			m.setToast("latency "+key+": "+reason, true)
		} else {
			m.setToast("latency "+key+": "+humanMillis(msg.data.Ms)+tcpNote(msg.data), false)
		}
		m.rebuildRows()
		return m, nil

	case actionDoneMsg:
		m.busy = ""
		if msg.err != nil {
			m.setToast(msg.label+": "+errText(msg.err), true)
		} else {
			m.setToast(msg.label, false)
		}
		// Refresh immediately: the action changed something the Dashboard
		// shows (the active server, the mode, the engine pid).
		m.polling = true
		return m, pollStatusCmd(m.ctx, m.cli)

	case snapshotMsg:
		m.snap = msg.snap
		m.rebuildRows()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Anything else (spinner ticks, viewport scrolls) goes to the components.
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(msg)
	cmds = append(cmds, cmd)
	m.nodes, cmd = m.nodes.Update(msg)
	cmds = append(cmds, cmd)
	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// handleKey implements the key map. Global keys first, then the active screen.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The filter editor swallows everything while it is open.
	if m.logInput {
		switch key {
		case "esc", "ctrl+c":
			m.logInput = false
			m.logInputBuf = ""
			return m, nil
		case "enter":
			m.logFilter = m.logInputBuf
			m.logInput = false
			m.syncViewport()
			return m, nil
		case "backspace":
			if m.logInputBuf != "" {
				r := []rune(m.logInputBuf)
				m.logInputBuf = string(r[:len(r)-1])
			}
			return m, nil
		default:
			if len(msg.Runes) > 0 {
				m.logInputBuf += string(msg.Runes)
			}
			return m, nil
		}
	}

	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
		return m, nil
	case "esc":
		if m.showHelp {
			m.showHelp = false
		}
		return m, nil
	case "tab":
		return m.switchScreen(screen((int(m.screen) + 1) % len(screenNames))), nil
	case "shift+tab":
		return m.switchScreen(screen((int(m.screen) + len(screenNames) - 1) % len(screenNames))), nil
	case "1", "2", "3", "4":
		return m.switchScreen(screen(int(key[0] - '1'))), nil
	}

	switch m.screen {
	case screenDashboard:
		return m.handleDashboardKey(key)
	case screenNodes:
		return m.handleNodesKey(msg)
	case screenLogs:
		return m.handleLogsKey(msg, key)
	case screenSettings:
		return m.handleSettingsKey(key)
	}
	return m, nil
}

func (m Model) handleDashboardKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "r":
		if m.attached {
			m.polling = true
			return m, pollStatusCmd(m.ctx, m.cli)
		}
		m.attaching = true
		return m, attachCmd(m.ctx, m.opts)
	case "t":
		return m.startLatency("")
	case "s":
		return m.startEngineAction(control.CmdStart)
	case "x":
		return m.startEngineAction(control.CmdStop)
	case "R":
		return m.startEngineAction(control.CmdRestart)
	case "m":
		return m.cycleMode()
	}
	return m, nil
}

func (m Model) handleNodesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.nodes.MoveUp(1)
		return m, nil
	case "down", "j":
		m.nodes.MoveDown(1)
		return m, nil
	case "g", "home":
		m.nodes.GotoTop()
		return m, nil
	case "G", "end":
		m.nodes.GotoBottom()
		return m, nil
	case "enter":
		row := m.currentRow()
		if row == nil {
			return m, nil
		}
		if !m.attached {
			m.setToast("daemon not running: start it with `vender daemon`", true)
			return m, nil
		}
		if row.Active {
			m.setToast(row.Name+" is already active", false)
			return m, nil
		}
		m.busy = "switching to " + row.Name + "…"
		return m, selectServerCmd(m.ctx, m.opts, row.Name)
	case "t":
		row := m.currentRow()
		if row == nil {
			return m, nil
		}
		if !m.attached {
			// Without a daemon there is no tunnel, but a TCP reachability
			// probe is still useful; the daemon-side latency call needs the
			// engine, so this path reports exactly that.
			m.setToast("latency needs a running daemon", true)
			return m, nil
		}
		return m.startLatency(row.Name)
	case "r":
		cmds := []tea.Cmd{loadSnapshotCmd(m.opts)}
		if m.attached {
			m.polling = true
			cmds = append(cmds, pollStatusCmd(m.ctx, m.cli))
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m Model) handleLogsKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "f":
		m.logFollow = !m.logFollow
		if m.logFollow {
			m.view.GotoBottom()
		}
		return m, nil
	case "/":
		m.logInput = true
		m.logInputBuf = m.logFilter
		return m, nil
	case "t":
		m.logFilter = ""
		m.syncViewport()
		return m, nil
	}
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(msg)
	if key == "up" || key == "k" || key == "pgup" || key == "pgdown" || key == "down" || key == "j" {
		// Manual scrolling turns follow off, so the view stops jumping to the
		// bottom while the user is reading something.
		if !m.view.AtBottom() {
			m.logFollow = false
		}
	}
	return m, cmd
}

func (m Model) handleSettingsKey(key string) (tea.Model, tea.Cmd) {
	modes := config.ProxyModes()
	switch key {
	case "left", "h":
		m.settingsCursor = (m.settingsCursor + len(modes) - 1) % len(modes)
		return m, nil
	case "right", "l":
		m.settingsCursor = (m.settingsCursor + 1) % len(modes)
		return m, nil
	case "enter":
		return m.applyMode(modes[m.settingsCursor])
	case "m":
		return m.cycleMode()
	}
	return m, nil
}

func (m Model) startEngineAction(action string) (tea.Model, tea.Cmd) {
	if !m.attached {
		m.setToast("daemon not running: start it with `vender daemon`", true)
		return m, nil
	}
	m.busy = "engine " + action + "…"
	return m, engineCmd(m.ctx, m.opts, action)
}

func (m Model) startLatency(name string) (tea.Model, tea.Cmd) {
	if !m.attached {
		m.setToast("daemon not running: start it with `vender daemon`", true)
		return m, nil
	}
	label := name
	if label == "" {
		label = m.selected
	}
	m.busy = "testing " + label + "…"
	return m, latencyCmd(m.ctx, m.opts, name)
}

func (m Model) cycleMode() (tea.Model, tea.Cmd) {
	modes := config.ProxyModes()
	next := modes[(indexOfMode(config.ProxyMode(m.currentMode()))+1)%len(modes)]
	m.settingsCursor = indexOfMode(next)
	return m.applyMode(next)
}

func (m Model) applyMode(mode config.ProxyMode) (tea.Model, tea.Cmd) {
	if !m.attached {
		m.setToast("daemon not running: start it with `vender daemon`", true)
		return m, nil
	}
	if string(mode) == m.currentMode() {
		m.setToast("already in "+string(mode)+" mode", false)
		return m, nil
	}
	m.busy = "switching to " + string(mode) + " mode…"
	return m, setModeCmd(m.ctx, m.opts, string(mode))
}

// --- state helpers ----------------------------------------------------------

func (m *Model) resize(width, height int) {
	m.width, m.height = width, height
	inner := max(24, m.panelWidth()-4)
	m.nodes.SetColumns(nodeColumns(inner - 1))
	m.nodes.SetWidth(inner)
	m.nodes.SetHeight(max(3, min(len(m.servers)+1, height-8)))
	m.view.Width = inner
	m.view.Height = max(3, height-8)
	m.progress.Width = max(10, min(40, width/3))
	m.syncViewport()
	m.rebuildRows()
}

func (m *Model) switchScreen(s screen) Model {
	if s == m.screen {
		return *m
	}
	m.fromTab = int(m.screen)
	m.screen = s
	m.tabAnim = 0
	return *m
}

func (m *Model) setToast(text string, isErr bool) {
	m.toast = text
	m.toastErr = isErr
	m.toastTTL = toastTicks
}

func (m *Model) detach(err error) {
	closeStream(m.logStream)
	closeStream(m.trafStream)
	m.logStream, m.trafStream = nil, nil
	if m.cli != nil {
		_ = m.cli.Close()
		m.cli = nil
	}
	m.attached = false
	m.attachErr = errText(err)
}

func closeStream(s *control.Stream) {
	if s != nil {
		_ = s.Close()
	}
}

func (m *Model) appendLog(line control.LogLine) {
	m.logs = append(m.logs, line)
	if len(m.logs) > logBuffer {
		m.logs = append(m.logs[:0], m.logs[len(m.logs)-logBuffer:]...)
	}
	m.syncViewport()
	if m.logFollow {
		m.view.GotoBottom()
	}
}

func (m *Model) pushTraffic(s control.TrafficSample) {
	m.upRates = append(m.upRates, s.Up)
	m.downRates = append(m.downRates, s.Down)
	if len(m.upRates) > sampleWindow {
		m.upRates = append(m.upRates[:0], m.upRates[len(m.upRates)-sampleWindow:]...)
	}
	if len(m.downRates) > sampleWindow {
		m.downRates = append(m.downRates[:0], m.downRates[len(m.downRates)-sampleWindow:]...)
	}
	if s.TotalUp > 0 {
		m.totalUp = s.TotalUp
	}
	if s.TotalDown > 0 {
		m.totalDown = s.TotalDown
	}
}

// syncViewport rebuilds the log viewport content from the buffer and filter.
func (m *Model) syncViewport() {
	if m.view.Width == 0 {
		return
	}
	filter := strings.ToLower(m.logFilter)
	lines := make([]string, 0, len(m.logs))
	for _, l := range m.logs {
		if filter != "" && !strings.Contains(strings.ToLower(l.Line), filter) {
			continue
		}
		lines = append(lines, m.logLineView(l))
	}
	m.view.SetContent(strings.Join(lines, "\n"))
	m.viewportSync = true
	if m.logFollow {
		m.view.GotoBottom()
	}
}

// syncRowsFromStatus refreshes the Nodes table from the daemon's list.
func (m *Model) syncRowsFromStatus() {
	if m.status.Selected != "" {
		m.selected = m.status.Selected
	}
	m.rebuildRows()
}

// rebuildRows merges the two sources of truth: servers.yaml (always available)
// and the daemon's status (which server is active, what it is doing).
func (m *Model) rebuildRows() {
	rows := make([]serverRow, 0, len(m.snap.Servers))
	for i := range m.snap.Servers {
		srv := &m.snap.Servers[i]
		rows = append(rows, serverRow{
			Name:     srv.Name,
			Type:     string(srv.Type),
			Protocol: srv.Detail(),
			Endpoint: srv.Endpoint(),
			Active:   strings.EqualFold(srv.Name, m.selectedName()),
			Latency:  m.latencyText(srv.Name),
		})
	}
	m.servers = rows
	m.nodes.SetRows(m.nodeTableRows())
	// SetRows only clamps the cursor downwards, so a table that was empty
	// (cursor -1) would never move on its own; SetCursor clamps both ways.
	m.nodes.SetCursor(max(0, m.nodes.Cursor()))
	m.nodes.SetHeight(max(3, min(len(m.servers)+1, max(4, m.height-8))))
}

func (m *Model) selectedName() string {
	if m.attached && m.status.Selected != "" {
		return m.status.Selected
	}
	return m.snap.Selected
}

func (m *Model) currentRow() *serverRow {
	if len(m.servers) == 0 {
		return nil
	}
	i := m.nodes.Cursor()
	if i < 0 || i >= len(m.servers) {
		return nil
	}
	return &m.servers[i]
}

func (m *Model) latencyText(name string) string {
	d, ok := m.latency[name]
	if !ok {
		return ""
	}
	return latencyText(d) + tcpNote(d)
}

func latencyText(d control.LatencyData) string {
	if d.Ms == 0 {
		if d.Err != "" {
			return "failed"
		}
		return "-"
	}
	return humanMillis(d.Ms)
}

// tcpNote marks a measurement that did not go through the tunnel. It is
// deliberately not an error: a bare TCP handshake is the best vender can do
// for a server that is not the active one (vender has a single outbound), and
// on a host where a TUN hijacks outbound TCP the number can be optimistic to
// the point of being meaningless.
func tcpNote(d control.LatencyData) string {
	if d.Ms == 0 || d.Err == "" {
		return ""
	}
	return " (tcp)"
}

func humanMillis(ms int) string {
	if ms < 1000 {
		return strconv.Itoa(ms) + " ms"
	}
	return strconv.Itoa(ms/1000) + "." + strconv.Itoa((ms%1000)/100) + " s"
}

func errText(err error) string {
	if err == nil {
		return "closed"
	}
	return err.Error()
}

func indexOfMode(m config.ProxyMode) int {
	for i, x := range config.ProxyModes() {
		if x == m {
			return i
		}
	}
	return 0
}

func (m Model) currentMode() string {
	if m.attached && m.status.Mode != "" {
		return m.status.Mode
	}
	return m.snap.Mode
}
