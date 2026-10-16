package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"sync"

	"github.com/ll-lite/vender/internal/control"
	"github.com/ll-lite/vender/internal/core"
)

// daemonHarness runs a real `vender daemon` in this process, with the test
// binary standing in for sing-box (see fake_singbox_test.go). Everything above
// the fake engine is production code: the lock, the pid file, the controller,
// the generated config, the Clash API client used for traffic and latency, and
// the control server.
type daemonHarness struct {
	cfgDir   string
	runDir   string
	cancel   context.CancelFunc
	done     chan error
	output   *syncBuffer
	endpoint control.Endpoint
}

// syncBuffer is what the daemon writes its stdout into. It is read by the test
// while the daemon is still running, so both ends need the lock; the daemon's
// own writer only serialises its several output goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startDaemon(t *testing.T, listen string, links ...string) *daemonHarness {
	t.Helper()
	cfgDir, runDir := t.TempDir(), t.TempDir()

	for i, link := range links {
		name := fmt.Sprintf("Node-%c", 'A'+i)
		out, err := runCLIIn(t, runDir, "import", link, "--name", name, "--config-dir", cfgDir)
		if err != nil {
			t.Fatalf("import %d: %v\n%s", i, err, out)
		}
	}
	// Point the engine at this test binary and make it act as sing-box.
	store := config.NewStoreAt(cfgDir)
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Engine.Binary = os.Args[0]
	cfg.Proxy.Listen = listen
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	t.Setenv(fakeEngineEnv, "ok")

	h := &daemonHarness{cfgDir: cfgDir, runDir: runDir, done: make(chan error, 1), output: &syncBuffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel

	cmd := newRootCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(h.output)
	cmd.SetErr(h.output)
	cmd.SetArgs([]string{"--run-dir", runDir, "daemon", "--config-dir", cfgDir})
	go func() { h.done <- cmd.Execute() }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		ep, err := control.ReadEndpoint(runDir)
		if err == nil {
			h.endpoint = ep
			return h
		}
		if !control.IsNotRunning(err) {
			t.Fatalf("ReadEndpoint: %v", err)
		}
		select {
		case err := <-h.done:
			t.Fatalf("the daemon exited during startup: %v\n%s", err, h.output)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never published a control endpoint\n%s", h.output)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *daemonHarness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("daemon exited with %v\n%s", err, h.output)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the daemon did not stop\n%s", h.output)
	}
}

func (h *daemonHarness) dial(t *testing.T) *control.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := control.Dial(ctx, h.runDir)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// TestDaemonControlChannelEndToEnd drives a real daemon through the real
// control channel: status, servers, select (which restarts the engine in
// place), latency from the engine's Clash API, live logs and live traffic.
func TestDaemonControlChannelEndToEnd(t *testing.T) {
	listen := "127.0.0.1:" + fmt.Sprint(freePort(t))
	h := startDaemon(t, listen, vlessLink, hy2Link)
	defer h.stop(t)

	cli := h.dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ping, err := cli.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if ping.Proto != control.Version || ping.RunDir != h.runDir {
		t.Errorf("ping = %+v", ping)
	}

	st, err := cli.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.EngineRunning || !st.EngineHealthy {
		t.Fatalf("status = %+v, want a running, healthy engine", st)
	}
	if st.Listen != listen || st.Selected != "Node-A" || st.Mode != "rule" {
		t.Errorf("status = %+v", st)
	}
	if st.EnginePid == 0 || st.ServerCount != 2 {
		t.Errorf("status = %+v, want a child pid and two servers", st)
	}

	// The engine must have been started with the Clash API enabled, on the
	// very address the controller reports.
	generated, err := os.ReadFile(core.ConfigPath(h.runDir))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if !strings.Contains(string(generated), st.ClashListen) {
		t.Errorf("generated config does not mention the clash api at %s", st.ClashListen)
	}

	servers, err := cli.Servers(ctx)
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(servers.Servers) != 2 || servers.Selected != "Node-A" {
		t.Fatalf("servers = %+v", servers)
	}

	// Latency is measured through the fake engine's Clash API delay endpoint.
	lat, err := cli.Latency(ctx, "")
	if err != nil {
		t.Fatalf("Latency: %v", err)
	}
	if lat.Ms != fakeDelayMs {
		t.Errorf("latency = %+v, want %d ms from the engine's delay probe", lat, fakeDelayMs)
	}

	// Live logs: the daemon replays the engine's output to an attaching
	// client, and reports lifecycle notes of its own.
	logCli := h.dial(t)
	logs, err := logCli.FollowLogs(ctx)
	if err != nil {
		t.Fatalf("FollowLogs: %v", err)
	}
	defer logs.Close()
	sawEngineLine, sawStartNote := false, false
	for !(sawEngineLine && sawStartNote) {
		var line control.LogLine
		if err := logs.Next(ctx, &line); err != nil {
			t.Fatalf("Next(log): %v", err)
		}
		if line.Source == control.SourceEngine && strings.Contains(line.Line, "mixed server started") {
			sawEngineLine = true
		}
		if line.Source == control.SourceDaemon && strings.Contains(line.Line, "engine started") {
			sawStartNote = true
		}
	}

	// Live traffic, straight from the engine's Clash API through the daemon.
	trafCli := h.dial(t)
	traffic, err := trafCli.WatchTraffic(ctx)
	if err != nil {
		t.Fatalf("WatchTraffic: %v", err)
	}
	defer traffic.Close()
	var first, second control.TrafficSample
	if err := traffic.Next(ctx, &first); err != nil {
		t.Fatalf("Next(traffic): %v", err)
	}
	if err := traffic.Next(ctx, &second); err != nil {
		t.Fatalf("Next(traffic): %v", err)
	}
	if second.Down <= first.Down || second.Up <= first.Up {
		t.Errorf("traffic does not move: %+v then %+v", first, second)
	}
	if second.TotalDown == 0 {
		t.Errorf("traffic totals are missing: %+v", second)
	}

	// Selecting a server restarts the engine in place: the child pid changes,
	// the selection is persisted, and traffic keeps flowing afterwards.
	before := st.EnginePid
	applied, err := cli.Select(ctx, "Node-B")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if applied.Status.Selected != "Node-B" {
		t.Errorf("select reply = %+v", applied.Status)
	}
	if applied.Status.EnginePid == before || applied.Status.EnginePid == 0 {
		t.Errorf("engine pid did not change across a select (was %d, now %d)", before, applied.Status.EnginePid)
	}
	stored, err := config.NewStoreAt(h.cfgDir).LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if stored.SelectedServer != "Node-B" {
		t.Errorf("selected_server = %q, want Node-B to be persisted", stored.SelectedServer)
	}
	var after control.TrafficSample
	if err := traffic.Next(ctx, &after); err != nil {
		t.Fatalf("traffic after select: %v", err)
	}
	if after.Down == 0 {
		t.Errorf("traffic stopped after the restart: %+v", after)
	}

	// Mode switching goes through the same path and is reflected in the
	// rendered config.
	applied, err = cli.SetMode(ctx, "global")
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if applied.Status.Mode != "global" {
		t.Errorf("mode = %q", applied.Status.Mode)
	}
	rules, err := cli.Rules(ctx)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if rules.Active {
		t.Error("global mode reported the built-in policy as active")
	}

	// Stop/start of the engine inside the running daemon.
	if _, err := cli.StopEngine(ctx); err != nil {
		t.Fatalf("StopEngine: %v", err)
	}
	if st, err := cli.Status(ctx); err != nil {
		t.Fatalf("Status: %v", err)
	} else if st.EngineRunning {
		t.Error("the engine is still running after stop")
	}
	// The daemon itself is untouched by an engine stop.
	if _, err := cli.Ping(ctx); err != nil {
		t.Fatalf("ping after engine stop: %v", err)
	}
	if _, err := cli.StartEngine(ctx); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}
	if st, err := cli.Status(ctx); err != nil {
		t.Fatalf("Status: %v", err)
	} else if !st.EngineRunning {
		t.Error("the engine did not come back after start")
	}
}

func TestDaemonControlChannelRejectsABadToken(t *testing.T) {
	listen := "127.0.0.1:" + fmt.Sprint(freePort(t))
	h := startDaemon(t, listen, vlessLink)
	defer h.stop(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	bad, err := control.DialAddr(ctx, h.endpoint.Addr, "0000000000000000000000000000dead")
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer bad.Close()
	_, err = bad.Status(ctx)
	if err == nil {
		t.Fatal("a forged token was accepted")
	}
	if control.KindOf(err) != control.KindAuth {
		t.Errorf("kind = %q, want auth (error %v)", control.KindOf(err), err)
	}

	good := h.dial(t)
	if _, err := good.Status(ctx); err != nil {
		t.Fatalf("the daemon stopped serving after rejecting a peer: %v", err)
	}
}

// TestDaemonPublishesAndWithdrawsItsEndpoint: the endpoint file is the only
// discovery mechanism, so its lifetime is part of the contract.
func TestDaemonPublishesAndWithdrawsItsEndpoint(t *testing.T) {
	listen := "127.0.0.1:" + fmt.Sprint(freePort(t))
	h := startDaemon(t, listen, vlessLink)

	if _, err := os.Stat(control.EndpointPath(h.runDir)); err != nil {
		t.Fatalf("control.json is missing: %v", err)
	}
	if !strings.Contains(h.output.String(), "listening on "+listen) {
		t.Errorf("daemon output does not announce the listener:\n%s", h.output)
	}
	if !strings.Contains(h.output.String(), "control on 127.0.0.1:") {
		t.Errorf("daemon output does not announce the control endpoint:\n%s", h.output)
	}

	h.stop(t)

	if _, err := control.ReadEndpoint(h.runDir); !control.IsNotRunning(err) {
		t.Errorf("the endpoint survived the daemon: %v", err)
	}
	if _, err := os.Stat(core.PidPath(h.runDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the pid file survived the daemon: %v", err)
	}
	// The port is free again, so a restart cannot be blocked by a listener the
	// control channel forgot to close.
	if _, err := os.Stat(control.EndpointPath(h.runDir)); err == nil {
		t.Error("control.json still exists after shutdown")
	}
}

// TestDaemonTunCommand drives the TUN command through the real control channel.
//
// There are exactly two legitimate outcomes, and the test accepts both because
// which one happens depends on where the suite runs: either this process can
// create a TUN device (root, or a capability) and the daemon re-renders and
// restarts the engine with the TUN inbound, or it cannot — and then the daemon
// must refuse *before* touching the engine, leave the configuration alone, and
// say what is missing.
func TestDaemonTunCommand(t *testing.T) {
	listen := "127.0.0.1:" + fmt.Sprint(freePort(t))
	h := startDaemon(t, listen, vlessLink)
	defer h.stop(t)

	cli := h.dial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := cli.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Tun.Enabled || st.Tun.Active {
		t.Errorf("a fresh daemon already has TUN on: %+v", st.Tun)
	}
	// The privilege verdict always travels with the status, because a client's
	// own verdict is not the daemon's.
	if st.Tun.PrivilegeDetail == "" {
		t.Error("status carries no TUN privilege detail")
	}

	// Turning off what is already off is a no-op that still answers.
	if applied, err := cli.SetTun(ctx, false); err != nil {
		t.Fatalf("SetTun(false): %v", err)
	} else if applied.Status.Tun.Enabled {
		t.Errorf("TUN is on after SetTun(false): %+v", applied.Status.Tun)
	}

	applied, err := cli.SetTun(ctx, true)
	if err != nil {
		// The refusal path.
		msg := err.Error()
		if !strings.Contains(msg, "CAP_NET_ADMIN") && !strings.Contains(strings.ToLower(msg), "administrator") {
			t.Errorf("a refused TUN enable does not name the privilege: %v", err)
		}
		if kind := control.KindOf(err); kind != control.KindBadRequest {
			t.Errorf("kind = %q, want bad_request (a refusal, not an internal failure)", kind)
		}
		// Nothing was persisted, and the engine was not stopped by the attempt.
		cfg, cerr := config.NewStoreAt(h.cfgDir).LoadConfig()
		if cerr != nil {
			t.Fatalf("LoadConfig: %v", cerr)
		}
		if cfg.Engine.Tun.Enabled {
			t.Error("a refused enable changed config.yaml")
		}
		st, serr := cli.Status(ctx)
		if serr != nil {
			t.Fatalf("Status after a refusal: %v", serr)
		}
		if !st.EngineRunning {
			t.Error("a refused enable left the engine down")
		}
		if strings.Contains(h.output.String(), "vender: tun on") {
			t.Errorf("the refusal was recorded as a change:\n%s", h.output)
		}
		return
	}

	// The privileged path: persisted, applied, and the engine restarted with a
	// TUN inbound.
	if !applied.Status.Tun.Enabled || !applied.Status.Tun.Active {
		t.Errorf("state after SetTun(true) = %+v", applied.Status.Tun)
	}
	cfg, cerr := config.NewStoreAt(h.cfgDir).LoadConfig()
	if cerr != nil {
		t.Fatalf("LoadConfig: %v", cerr)
	}
	if !cfg.Engine.Tun.Enabled {
		t.Error("SetTun(true) did not persist the setting")
	}
	if !applied.Status.EngineHealthy {
		t.Error("the engine is not healthy after the TUN restart")
	}

	// And back off: the TUN inbound disappears from the rendered config.
	if applied, err := cli.SetTun(ctx, false); err != nil {
		t.Fatalf("SetTun(false): %v", err)
	} else if applied.Status.Tun.Enabled || applied.Status.Tun.Active {
		t.Errorf("state after SetTun(false) = %+v", applied.Status.Tun)
	}
	if cfg, cerr = config.NewStoreAt(h.cfgDir).LoadConfig(); cerr != nil {
		t.Fatalf("LoadConfig: %v", cerr)
	}
	if cfg.Engine.Tun.Enabled {
		t.Error("SetTun(false) did not persist the setting")
	}
}

// The CLI's `tun on` against a running daemon ends in one of the same two
// states, and must never leave the daemon with a broken configuration.
func TestCLITunOnAgainstARunningDaemon(t *testing.T) {
	listen := "127.0.0.1:" + fmt.Sprint(freePort(t))
	h := startDaemon(t, listen, vlessLink)
	defer h.stop(t)

	out, err := runCLIIn(t, h.runDir, "tun", "on", "--config-dir", h.cfgDir)
	cfg, cerr := config.NewStoreAt(h.cfgDir).LoadConfig()
	if cerr != nil {
		t.Fatalf("LoadConfig: %v", cerr)
	}
	if err != nil {
		// Refused: the setting must not have been left on.
		if cfg.Engine.Tun.Enabled {
			t.Errorf("a refused `tun on` persisted the setting (output %q)", out)
		}
		if !strings.Contains(out+err.Error(), "CAP_NET_ADMIN") && !strings.Contains(strings.ToLower(err.Error()), "administrator") {
			t.Errorf("the failure does not name the privilege: %v\n%s", err, out)
		}
		if !strings.Contains(err.Error(), "engine keeps running") {
			t.Errorf("the failure does not say the engine is intact: %v", err)
		}
	} else if !cfg.Engine.Tun.Enabled {
		t.Error("`tun on` reported success but did not persist the setting")
	}

	// Either way the daemon is still serving: `status` exits 0.
	if out, err := runCLIIn(t, h.runDir, "status", "--config-dir", h.cfgDir); err != nil {
		t.Errorf("status after `tun on`: %v\n%s", err, out)
	}
}
