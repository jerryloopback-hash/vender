package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
	"github.com/ll-lite/vender/internal/privatefs"
)

// Dummy credentials only: never commit real ones.
const (
	testUUID = "11111111-1111-1111-1111-111111111111"
	testPBK  = "DUMMY-FAKE-PUBLIC-KEY-FOR-TESTS-ONLY-0000000000"
	testSID  = "0123456789abcdef"
	testHost = "203.0.113.10"
)

// fakeEngine stands in for core's sing-box supervisor. It reproduces the parts
// the controller depends on: the log channel is replaced on each start and
// closed when the run ends, and Stop is what closes it.
type fakeEngine struct {
	mu         sync.Mutex
	writes     []core.RuntimeConfig
	starts     int
	stops      int
	logs       chan string
	logsDone   bool
	running    bool
	healthy    bool
	startErr   error
	startDelay time.Duration
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{logs: make(chan string, 64), healthy: true}
}

func (f *fakeEngine) WriteConfig(rc core.RuntimeConfig) error {
	if err := rc.Validate(); err != nil {
		return err
	}
	// Mirror the one write the real engine performs: render the config into
	// the run dir, so tests can assert on the bytes the engine would run with.
	data, err := core.BuildConfig(rc)
	if err != nil {
		return err
	}
	if err := privatefs.EnsureDir(rc.RunDir); err != nil {
		return err
	}
	if err := os.WriteFile(core.ConfigPath(rc.RunDir), data, 0o600); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, rc)
	return nil
}

func (f *fakeEngine) Start() error {
	f.mu.Lock()
	delay, err := f.startDelay, f.startErr
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		return err
	}
	if f.logsDone {
		f.logs = make(chan string, 64)
		f.logsDone = false
	}
	f.starts++
	f.running = true
	return nil
}

func (f *fakeEngine) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return nil
	}
	f.running = false
	f.stops++
	f.logsDone = true
	close(f.logs)
	return nil
}

func (f *fakeEngine) Logs() <-chan string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs
}

func (f *fakeEngine) Healthy() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running && f.healthy
}

func (f *fakeEngine) Pid() int { return 4242 }

func (f *fakeEngine) emit(line string) {
	f.mu.Lock()
	ch := f.logs
	f.mu.Unlock()
	ch <- line
}

func (f *fakeEngine) state() (int, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops, f.running
}

func (f *fakeEngine) lastWrite() core.RuntimeConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return core.RuntimeConfig{}
	}
	return f.writes[len(f.writes)-1]
}

// --- fixtures ---------------------------------------------------------------

type fixture struct {
	ctrl   *Controller
	engine *fakeEngine
	store  *config.Store
	cfgDir string
	runDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfgDir, runDir := t.TempDir(), t.TempDir()
	store := config.NewStoreAt(cfgDir)
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	vless := config.Server{
		Name: "Node-A", Type: config.TypeVLESS, Address: testHost, Port: 443,
		UUID: testUUID, Security: config.SecurityReality, SNI: "www.example.com",
		PublicKey: testPBK, ShortID: testSID, Fingerprint: "chrome",
	}
	hy2 := config.Server{
		Name: "Node-B", Type: config.TypeHysteria2, Address: "203.0.113.11", Port: 8443,
		Password: "password", SNI: "www.bing.com", Insecure: true,
	}
	ss := config.NewServerSet()
	if err := ss.Add(vless, false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ss.Add(hy2, false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatalf("SaveServers: %v", err)
	}
	cfg := config.Default()
	cfg.SelectedServer = "Node-A"
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	eng := newFakeEngine()
	ctrl, err := NewController(ControllerOptions{
		Store: store, RunDir: runDir, Engine: eng, BinaryPath: "/bin/true", Version: "test",
		ClashListen: "127.0.0.1:1",
		Probe: func(context.Context, core.PingRequest) (core.PingResult, error) {
			return core.PingResult{Ms: 17, Sent: 3, Received: 2, PacketLoss: 33}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	t.Cleanup(ctrl.Close)
	return &fixture{ctrl: ctrl, engine: eng, store: store, cfgDir: cfgDir, runDir: runDir}
}

// --- tests ------------------------------------------------------------------

func TestControllerStartStopAndStatus(t *testing.T) {
	f := newFixture(t)

	st := f.ctrl.Status()
	if st.EngineRunning {
		t.Fatal("a fresh controller reports a running engine")
	}
	if st.Mode != string(config.ModeRule) || st.Selected != "Node-A" || st.ServerCount != 2 {
		t.Errorf("status = %+v, want the fixture's mode/selection/count", st)
	}
	if st.SrvType != "vless" || st.SrvEndpoint != testHost+":443" {
		t.Errorf("status server = %q %q", st.SrvType, st.SrvEndpoint)
	}

	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}
	// The generated config must have been written with the Clash API enabled:
	// that is the daemon's whole reason for holding a Clash client.
	data, err := os.ReadFile(core.ConfigPath(f.runDir))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if !strings.Contains(string(data), `"clash_api"`) || !strings.Contains(string(data), f.ctrl.ClashListen()) {
		t.Errorf("generated config has no clash_api section:\n%s", data)
	}
	// ...and it must stay 0600: it carries both the server credentials and the
	// Clash API secret.
	info, err := os.Stat(core.ConfigPath(f.runDir))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("generated config mode = %o, want 600", perm)
	}

	st = f.ctrl.Status()
	if !st.EngineRunning || !st.EngineHealthy || st.EnginePid != 4242 {
		t.Errorf("status after start = %+v", st)
	}
	if st.UptimeSecs < 0 {
		t.Errorf("uptime = %d", st.UptimeSecs)
	}

	if err := f.ctrl.StopEngine(); err != nil {
		t.Fatalf("StopEngine: %v", err)
	}
	if st = f.ctrl.Status(); st.EngineRunning || st.EnginePid != 0 {
		t.Errorf("status after stop = %+v", st)
	}
	starts, stops, _ := f.engine.state()
	if starts != 1 || stops != 1 {
		t.Errorf("engine starts/stops = %d/%d, want 1/1", starts, stops)
	}
}

func TestControllerSelectRestartsAndPersists(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}

	if err := f.ctrl.SelectServer("Node-B"); err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	starts, stops, running := f.engine.state()
	if starts != 2 || stops != 1 || !running {
		t.Fatalf("engine starts/stops/running = %d/%d/%v, want 2/1/true", starts, stops, running)
	}
	if got := f.engine.lastWrite().Server.Name; got != "Node-B" {
		t.Errorf("engine was restarted with %q, want Node-B", got)
	}
	if st := f.ctrl.Status(); st.Selected != "Node-B" {
		t.Errorf("status selected = %q", st.Selected)
	}
	// The choice is persisted, so the next `vender daemon` honours it.
	cfg, err := f.store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "Node-B" {
		t.Errorf("config.yaml selected_server = %q, want Node-B", cfg.SelectedServer)
	}

	if err := f.ctrl.SelectServer("nope"); err == nil {
		t.Fatal("selecting an unknown server = nil error")
	} else if KindOf(err) != KindNotFound {
		t.Errorf("kind = %q, want %q", KindOf(err), KindNotFound)
	}
}

func TestControllerModeSwitch(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}

	if err := f.ctrl.SetMode("global"); err != nil {
		t.Fatalf("SetMode(global): %v", err)
	}
	if got := f.engine.lastWrite().Config.Proxy.Mode; got != config.ModeGlobal {
		t.Errorf("engine rendered mode %q, want global", got)
	}

	// A no-op mode change must not restart the engine (that would drop the
	// connection for nothing).
	before, _, _ := f.engine.state()
	if err := f.ctrl.SetMode("GLOBAL"); err != nil {
		t.Fatalf("SetMode(GLOBAL): %v", err)
	}
	if after, _, _ := f.engine.state(); after != before {
		t.Errorf("a no-op mode change restarted the engine (%d -> %d starts)", before, after)
	}

	// direct mode drops the proxy outbound entirely and does not need a server.
	if err := f.ctrl.SetMode("direct"); err != nil {
		t.Fatalf("SetMode(direct): %v", err)
	}
	rc := f.engine.lastWrite()
	if rc.Server != nil {
		t.Errorf("direct mode rendered a server: %+v", rc.Server)
	}
	data, err := os.ReadFile(core.ConfigPath(f.runDir))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), `"tag": "proxy"`) {
		t.Errorf("direct mode still renders a proxy outbound:\n%s", data)
	}

	if err := f.ctrl.SetMode("sideways"); err == nil {
		t.Fatal("SetMode(sideways) = nil error")
	} else if KindOf(err) != KindBadRequest {
		t.Errorf("kind = %q, want %q", KindOf(err), KindBadRequest)
	}
}

func TestControllerModeChangeWhileStoppedOnlyReRenders(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.SetMode("global"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	starts, _, _ := f.engine.state()
	if starts != 0 {
		t.Errorf("a stopped engine was started by a mode change (%d starts)", starts)
	}
	// The next start must use the new mode.
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}
	if got := f.engine.lastWrite().Config.Proxy.Mode; got != config.ModeGlobal {
		t.Errorf("rendered mode = %q, want global", got)
	}
}

func TestControllerLogStreamReplaysHistory(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}

	// A subscriber that attaches after some output has already been produced
	// must still see it: the TUI is started whenever the user feels like it.
	early, cancelEarly, err := f.ctrl.Subscribe(StreamLogs)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancelEarly()

	f.engine.emit("sing-box: hello")
	deadline := time.After(2 * time.Second)
	var sawHello bool
	for !sawHello {
		select {
		case v := <-early:
			if ll, ok := v.(LogLine); ok && strings.Contains(ll.Line, "hello") {
				sawHello = true
			}
		case <-deadline:
			t.Fatal("the live log line never arrived")
		}
	}

	late, cancelLate, err := f.ctrl.Subscribe(StreamLogs)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancelLate()
	// A late subscriber is handed the buffered history first; the live line is
	// somewhere in it.
	deadline = time.After(2 * time.Second)
	sawHistory := false
	for !sawHistory {
		select {
		case v := <-late:
			if ll, ok := v.(LogLine); ok && strings.Contains(ll.Line, "hello") {
				sawHistory = true
			}
		case <-deadline:
			t.Fatal("a late subscriber received no history")
		}
	}

	// Cancelling unregisters and closes the channel, which is how the server's
	// event pump terminates.
	cancelLate()
	select {
	case _, ok := <-late:
		if ok {
			// Drain any buffered frames first.
			for range late {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancelling a subscription did not close its channel")
	}
}

func TestControllerTrafficStreamAndTotals(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}
	ch, cancel, err := f.ctrl.Subscribe(StreamTraffic)
	if err != nil {
		t.Fatalf("Subscribe(traffic): %v", err)
	}
	defer cancel()

	f.ctrl.publishTraffic(TrafficSample{Up: 100, Down: 2048, TotalUp: 10, TotalDown: 20})
	select {
	case v := <-ch:
		s := v.(TrafficSample)
		if s.Up != 100 || s.Down != 2048 {
			t.Errorf("sample = %+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no traffic sample arrived")
	}
}

func TestControllerRulesTracksMode(t *testing.T) {
	f := newFixture(t)

	r := f.ctrl.Rules()
	if !r.Active {
		t.Error("rule mode should report an active policy")
	}
	if len(r.Actions) != 2 || len(r.Rules) < 3 {
		t.Errorf("rule mode policy = %d actions, %d rules", len(r.Actions), len(r.Rules))
	}
	if r.Rules[0].Name != "critical-direct" {
		t.Errorf("first rule = %q, want the critical list first (order is the policy)", r.Rules[0].Name)
	}
	if r.Rules[1].Name != "private-dest-direct" {
		t.Errorf("second rule = %q, want private-dest-direct", r.Rules[1].Name)
	}
	if len(r.RuleSets) == 0 || r.Source == "" {
		t.Errorf("rule sets/source missing: %+v", r)
	}

	if err := f.ctrl.SetMode("direct"); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	r = f.ctrl.Rules()
	if r.Active || len(r.Rules) != 0 {
		t.Errorf("direct mode reports a policy: %+v", r)
	}
	if r.Final != "direct" {
		t.Errorf("direct mode final = %q", r.Final)
	}
}

func TestControllerLatencyInDirectMode(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.SetMode("direct"); err != nil {
		t.Fatal(err)
	}
	res := f.ctrl.Latency(context.Background(), "Node-A")
	if res.Ms != 17 || res.Method != "icmp-direct" || res.Sent != 3 {
		t.Fatalf("direct ICMP: %+v", res)
	}
}

func TestControllerEngineFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.engine.mu.Lock()
	f.engine.startErr = errors.New("sing-box refused to start")
	f.engine.mu.Unlock()

	if err := f.ctrl.StartEngine(); err == nil {
		t.Fatal("StartEngine = nil error despite the injected failure")
	}
	st := f.ctrl.Status()
	if st.EngineRunning {
		t.Error("a failed start reported a running engine")
	}
	if !strings.Contains(st.LastError, "refused to start") {
		t.Errorf("LastError = %q", st.LastError)
	}
}

// newProfileFixture is newFixture plus a subscription profile: two
// hand-imported nodes (manual) and two provider nodes, with profiles.yaml
// present but inactive (so auto still scopes to manual until SetProfile).
func newProfileFixture(t *testing.T) *fixture {
	t.Helper()
	cfgDir, runDir := t.TempDir(), t.TempDir()
	store := config.NewStoreAt(cfgDir)
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	manual := config.Server{
		Name: "Hand-A", Type: config.TypeHysteria2, Address: testHost, Port: 8443,
		Password: "password", SNI: "www.bing.com", Insecure: true,
	}
	provA := config.Server{
		Name: "Prov-A", Type: config.TypeShadowsocks, Address: "203.0.113.20", Port: 8388,
		Method: "aes-256-gcm", Password: "dummy-ss-pass", Profile: "provider",
	}
	provB := config.Server{
		Name: "Prov-B", Type: config.TypeTrojan, Address: "203.0.113.21", Port: 443,
		Password: "dummy-trojan-pass", Security: config.SecurityTLS, SNI: "cdn.example.com",
		Fingerprint: "chrome", Network: "tcp", Profile: "provider",
	}
	ss := config.NewServerSet()
	for _, s := range []config.Server{manual, provA, provB} {
		if err := ss.Add(s, false); err != nil {
			t.Fatalf("Add(%s): %v", s.Name, err)
		}
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatalf("SaveServers: %v", err)
	}
	ps := config.NewProfileSet()
	ps.Upsert(config.Profile{Name: "provider", URL: "https://sub.example.com/x", FetchedAt: time.Now()})
	if err := store.SaveProfiles(ps); err != nil {
		t.Fatalf("SaveProfiles: %v", err)
	}
	cfg := config.Default()
	cfg.SelectedServer = config.SelectedServerAuto
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	eng := newFakeEngine()
	ctrl, err := NewController(ControllerOptions{
		Store: store, RunDir: runDir, Engine: eng, BinaryPath: "/bin/true", Version: "test",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	t.Cleanup(ctrl.Close)
	return &fixture{ctrl: ctrl, engine: eng, store: store, cfgDir: cfgDir, runDir: runDir}
}

// TestControllerProfileSwitchScopesAuto is the M7 rule in code: with
// selected_server "auto", switching the active profile switches which servers
// the engine's URLTest group is built from — and nothing else.
func TestControllerProfileSwitchScopesAuto(t *testing.T) {
	f := newProfileFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}

	// The default active profile is manual, so the group has one member.
	if got := f.engine.lastWrite().Servers; len(got) != 1 || got[0].Name != "Hand-A" {
		t.Fatalf("default auto pool = %v, want [Hand-A]", serverNames(got))
	}
	st := f.ctrl.Status()
	if st.Profile != config.ProfileManual || st.ProfileServers != 1 {
		t.Errorf("status profile = %q/%d, want manual/1", st.Profile, st.ProfileServers)
	}

	// Switching profiles restarts the engine with the new pool.
	if err := f.ctrl.SetProfile("provider"); err != nil {
		t.Fatalf("SetProfile: %v", err)
	}
	got := serverNames(f.engine.lastWrite().Servers)
	if len(got) != 2 || got[0] != "Prov-A" || got[1] != "Prov-B" {
		t.Fatalf("auto pool after SetProfile = %v, want [Prov-A Prov-B]", got)
	}
	st = f.ctrl.Status()
	if st.Profile != "provider" || st.ProfileServers != 2 {
		t.Errorf("status profile = %q/%d, want provider/2", st.Profile, st.ProfileServers)
	}
	// The registry's URL is not leaked through the status.
	if strings.Contains(fmt.Sprint(st.Profiles), "sub.example.com") {
		t.Errorf("status leaked a subscription URL: %v", st.Profiles)
	}
	// All three profiles are known to a client.
	if got := st.Profiles; len(got) != 2 || got[0] != config.ProfileManual || got[1] != "provider" {
		t.Errorf("status profiles = %v", got)
	}

	// A specific server still works regardless of the active profile.
	if err := f.ctrl.SelectServer("Hand-A"); err != nil {
		t.Fatalf("SelectServer(Hand-A): %v", err)
	}
	if srv := f.engine.lastWrite().Server; srv == nil || srv.Name != "Hand-A" {
		t.Errorf("single-server outbound = %v", srv)
	}
	if err := f.ctrl.SelectServer(config.SelectedServerAuto); err != nil {
		t.Fatalf("SelectServer(auto): %v", err)
	}

	// "all" widens the pool without touching the profiles themselves.
	if err := f.ctrl.SetProfile("all"); err != nil {
		t.Fatalf("SetProfile(all): %v", err)
	}
	if got := serverNames(f.engine.lastWrite().Servers); len(got) != 3 {
		t.Errorf("auto pool for all = %v, want 3 servers", got)
	}

	// A typo is refused before anything is written.
	if err := f.ctrl.SetProfile("ghost"); err == nil {
		t.Fatal("SetProfile(ghost) = nil error")
	} else if KindOf(err) != KindNotFound {
		t.Errorf("kind = %q, want not_found", KindOf(err))
	}
}

func serverNames(servers []config.Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

func TestControllerCallRoutesEveryCommand(t *testing.T) {
	f := newFixture(t)

	if _, err := f.ctrl.Call(context.Background(), "frobnicate", nil); err == nil {
		t.Fatal("unknown command = nil error")
	} else if KindOf(err) != KindBadRequest {
		t.Errorf("kind = %q, want bad_request", KindOf(err))
	}

	if _, err := f.ctrl.Call(context.Background(), CmdSelect, json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed args = nil error")
	} else if KindOf(err) != KindBadRequest {
		t.Errorf("kind = %q, want bad_request", KindOf(err))
	}

	if _, err := f.ctrl.Call(context.Background(), CmdSelect, nil); err == nil {
		t.Fatal("missing args = nil error")
	}

	data, err := f.ctrl.Call(context.Background(), CmdStart, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if apply, ok := data.(ApplyData); !ok || !apply.Status.EngineRunning {
		t.Errorf("start reply = %#v", data)
	}

	if _, err := f.ctrl.Call(context.Background(), CmdStop, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := f.ctrl.Call(context.Background(), CmdRestart, nil); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if st, err := f.ctrl.Call(context.Background(), CmdStatus, nil); err != nil {
		t.Fatalf("status: %v", err)
	} else if !st.(StatusData).EngineRunning {
		t.Error("restart did not leave the engine running")
	}
	if _, err := f.ctrl.Call(context.Background(), CmdPing, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestControllerLatencyAlwaysICMP(t *testing.T) {
	f := newFixture(t)
	var hosts []string
	f.ctrl.probe = func(ctx context.Context, req core.PingRequest) (core.PingResult, error) {
		hosts = append(hosts, req.Host)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("unbounded probe")
		}
		return core.PingResult{Ms: 17, Sent: 3, Received: 3}, nil
	}
	for _, selection := range []string{"Node-A", "Node-B", config.SelectedServerAuto} {
		f.ctrl.mu.Lock()
		f.ctrl.cfg.SelectedServer = selection
		f.ctrl.running = true
		f.ctrl.mu.Unlock()
		for _, name := range []string{"Node-A", "Node-B"} {
			res := f.ctrl.Latency(context.Background(), name)
			if res.Ms != 17 || res.Method != "icmp-direct" || res.Err != "" {
				t.Fatalf("%s/%s: %+v", selection, name, res)
			}
		}
	}
	if len(hosts) != 6 || hosts[1] != "203.0.113.11" {
		t.Fatalf("Hy2/selected/auto probes: %v", hosts)
	}
	if res := f.ctrl.Latency(context.Background(), "ghost"); !strings.Contains(res.Err, "no server named") {
		t.Fatal(res)
	}
}

// --- helpers ----------------------------------------------------------------

// listenOnce opens a loopback listener that accepts and closes connections,
// standing in for a reachable server.
func listenOnce(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			conn.Close()
		}
	}()
	return ln.Addr().String(), func() { ln.Close(); <-done }
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return host, port
}

// TestEndpointFileIsPrivate checks the two properties the security of the
// channel rests on: the endpoint file is 0600 and it is removed on exit.
func TestEndpointFileIsPrivate(t *testing.T) {
	runDir := t.TempDir()
	ep := Endpoint{V: Version, PID: 1, Addr: "127.0.0.1:1234", Token: "abc", RunDir: runDir}
	if err := WriteEndpoint(runDir, ep); err != nil {
		t.Fatalf("WriteEndpoint: %v", err)
	}
	info, err := os.Stat(filepath.Join(runDir, EndpointFileName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("control.json mode = %o, want 600 (it carries the token)", perm)
	}
	got, err := ReadEndpoint(runDir)
	if err != nil {
		t.Fatalf("ReadEndpoint: %v", err)
	}
	if got.Token != "abc" || got.Addr != "127.0.0.1:1234" {
		t.Errorf("round trip = %+v", got)
	}

	RemoveEndpoint(runDir)
	if _, err := ReadEndpoint(runDir); !IsNotRunning(err) {
		t.Errorf("ReadEndpoint after removal = %v, want IsNotRunning", err)
	}
}

func TestEndpointValidation(t *testing.T) {
	good := Endpoint{V: Version, Addr: "127.0.0.1:1", Token: "t"}
	if !good.Valid() {
		t.Error("a well-formed endpoint was rejected")
	}
	for name, ep := range map[string]Endpoint{
		"wrong version": {V: 99, Addr: "127.0.0.1:1", Token: "t"},
		"no token":      {V: Version, Addr: "127.0.0.1:1"},
		"not loopback":  {V: Version, Addr: "10.0.0.1:1", Token: "t"},
		"port zero":     {V: Version, Addr: "127.0.0.1:0", Token: "t"},
		"no port":       {V: Version, Addr: "127.0.0.1", Token: "t"},
	} {
		if ep.Valid() {
			t.Errorf("%s: Valid() = true, want false", name)
		}
	}
}

func TestAsStream(t *testing.T) {
	if s, ok := AsStream(StreamLogs); !ok || s != StreamLogs {
		t.Errorf("AsStream(logs) = %q, %v", s, ok)
	}
	if _, ok := AsStream(CmdStatus); ok {
		t.Error("status was treated as a stream")
	}
}

func TestKindOfDefaultsToInternal(t *testing.T) {
	if got := KindOf(errors.New("boom")); got != KindInternal {
		t.Errorf("KindOf = %q, want internal", got)
	}
	if got := KindOf(fmt.Errorf("wrapped: %w", Errorf(KindAuth, "nope"))); got != KindAuth {
		t.Errorf("KindOf = %q, want auth", got)
	}
}

// Proxies reports every configured server and marks exactly one active in
// single-server mode, even with no engine running.
func TestControllerProxiesSingleServer(t *testing.T) {
	f := newFixture(t)
	p := f.ctrl.Proxies(context.Background())
	if p.Auto {
		t.Error("single-server mode reported auto")
	}
	if len(p.Nodes) != 2 {
		t.Fatalf("Proxies returned %d nodes, want 2", len(p.Nodes))
	}
	active := 0
	for _, n := range p.Nodes {
		if n.Active {
			active++
			if n.Name != "Node-A" {
				t.Errorf("active node = %q, want Node-A", n.Name)
			}
		}
	}
	if active != 1 {
		t.Errorf("%d active nodes, want exactly 1", active)
	}
}

// Opting into auto failover is accepted through the control channel and flips
// the reported mode; the engine is not running, so the group has no "now" yet.
func TestControllerSelectAuto(t *testing.T) {
	f := newFixture(t)
	if err := f.ctrl.SelectServer(config.SelectedServerAuto); err != nil {
		t.Fatalf("SelectServer(auto): %v", err)
	}
	p := f.ctrl.Proxies(context.Background())
	if !p.Auto {
		t.Error("Proxies().Auto = false after selecting auto")
	}
	if p.Group != "proxy" {
		t.Errorf("group = %q, want the proxy tag", p.Group)
	}
	if len(p.Nodes) != 2 {
		t.Errorf("auto mode reported %d nodes, want 2", len(p.Nodes))
	}
	st := f.ctrl.Status()
	if st.Selected != config.SelectedServerAuto || st.SrvType != "auto" {
		t.Errorf("status = %q / %q, want auto", st.Selected, st.SrvType)
	}
}
