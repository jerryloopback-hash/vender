package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// serveFixture wires a fixture to a real Server over loopback and returns a
// client attached through the published endpoint — i.e. the whole channel,
// not just its halves.
func serveFixture(t *testing.T) (*fixture, *Server, *Client) {
	t.Helper()
	f := newFixture(t)
	srv, err := NewServer(ServerOptions{Handler: f.ctrl, RunDir: f.runDir, Version: "test"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := Dial(ctx, f.runDir)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return f, srv, cli
}

func TestServerPublishesAPrivateEndpoint(t *testing.T) {
	f, srv, _ := serveFixture(t)

	info, err := os.Stat(filepath.Join(f.runDir, EndpointFileName))
	if err != nil {
		t.Fatalf("the daemon did not publish %s: %v", EndpointFileName, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 600", EndpointFileName, perm)
	}
	ep, err := ReadEndpoint(f.runDir)
	if err != nil {
		t.Fatalf("ReadEndpoint: %v", err)
	}
	if ep.Addr != srv.Addr() || ep.Token != srv.Token() {
		t.Errorf("published endpoint = %+v, want addr %s", ep, srv.Addr())
	}
	if len(ep.Token) < 32 {
		t.Errorf("token = %q, want at least 128 bits of entropy", ep.Token)
	}
	host, _, _ := net.SplitHostPort(ep.Addr)
	if host != "127.0.0.1" {
		t.Errorf("endpoint host = %q, want loopback", host)
	}

	// Closing the server removes the endpoint, so a stale token cannot outlive
	// its daemon.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := ReadEndpoint(f.runDir); !IsNotRunning(err) {
		t.Errorf("ReadEndpoint after Close = %v, want IsNotRunning", err)
	}
}

func TestClientCallsOverTheChannel(t *testing.T) {
	f, _, cli := serveFixture(t)
	ctx := context.Background()

	ping, err := cli.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if ping.Proto != Version || ping.Vender != "test" || ping.PID != os.Getpid() {
		t.Errorf("ping = %+v", ping)
	}

	servers, err := cli.Servers(ctx)
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if len(servers.Servers) != 2 || servers.Selected != "Node-A" {
		t.Fatalf("servers = %+v", servers)
	}
	active := 0
	for _, s := range servers.Servers {
		if s.Active {
			active++
		}
	}
	if active != 1 {
		t.Errorf("%d servers marked active, want exactly 1", active)
	}

	applied, err := cli.StartEngine(ctx)
	if err != nil {
		t.Fatalf("StartEngine: %v", err)
	}
	if !applied.Status.EngineRunning || applied.Status.EnginePid != 4242 {
		t.Errorf("start reply = %+v", applied)
	}

	applied, err = cli.Select(ctx, "Node-B")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if applied.Status.Selected != "Node-B" {
		t.Errorf("select reply = %+v", applied.Status)
	}

	applied, err = cli.SetMode(ctx, "global")
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if applied.Status.Mode != "global" {
		t.Errorf("mode reply = %+v", applied.Status)
	}

	rules, err := cli.Rules(ctx)
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if rules.Active {
		t.Error("global mode reported an active built-in policy")
	}

	if _, err := cli.StopEngine(ctx); err != nil {
		t.Fatalf("StopEngine: %v", err)
	}
	if st, err := cli.Status(ctx); err != nil {
		t.Fatalf("Status: %v", err)
	} else if st.EngineRunning {
		t.Error("status still reports a running engine after stop")
	}

	// A protocol error must carry its kind, not just a message.
	if _, err := cli.Select(ctx, "ghost"); err == nil {
		t.Fatal("selecting an unknown server = nil error")
	} else if KindOf(err) != KindNotFound {
		t.Errorf("kind = %q, want not_found", KindOf(err))
	}
	if err := cli.Call(ctx, "bogus", nil, nil); KindOf(err) != KindBadRequest {
		t.Errorf("unknown command kind = %q, want bad_request", KindOf(err))
	}
	_ = f
}

func TestServerRejectsABadToken(t *testing.T) {
	f, srv, _ := serveFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bad, err := DialAddr(ctx, srv.Addr(), "not-the-token")
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer bad.Close()

	_, err = bad.Status(ctx)
	if err == nil {
		t.Fatal("a bad token was accepted")
	}
	if KindOf(err) != KindAuth || !strings.Contains(err.Error(), "token") {
		t.Errorf("error = %v (kind %q), want an auth error naming the token", err, KindOf(err))
	}

	// No token at all is rejected the same way.
	anon, err := DialAddr(ctx, srv.Addr(), "")
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer anon.Close()
	if _, err := anon.Status(ctx); KindOf(err) != KindAuth {
		t.Errorf("anonymous status error = %v (kind %q), want auth", err, KindOf(err))
	}

	// The authenticated client still works: a rejected peer must not take the
	// daemon down with it.
	good, err := Dial(ctx, f.runDir)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer good.Close()
	if _, err := good.Status(ctx); err != nil {
		t.Fatalf("status after a rejected peer: %v", err)
	}
}

func TestServerRejectsAProtocolVersionMismatch(t *testing.T) {
	_, srv, _ := serveFixture(t)

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Hand-rolled frame: this is the one place the raw wire format is
	// exercised directly, which is the point.
	_, err = conn.Write([]byte(`{"v":99,"auth":"` + srv.Token() + `","cmd":"status"}` + "\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if resp.OK || resp.Kind != KindAuth || !strings.Contains(resp.Error, "protocol version") {
		t.Errorf("response = %+v, want a version-mismatch auth error", resp)
	}
}

func TestServerStreamsLogsAndTraffic(t *testing.T) {
	f, srv, cli2 := serveFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logsCli, err := DialAddr(ctx, srv.Addr(), srv.Token())
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer logsCli.Close()
	stream, err := logsCli.FollowLogs(ctx)
	if err != nil {
		t.Fatalf("FollowLogs: %v", err)
	}
	defer stream.Close()

	f.engine.emit("sing-box: proxy/NODE-A: outbound connection to 1.2.3.4:443")
	var line LogLine
	if err := stream.Next(ctx, &line); err != nil {
		t.Fatalf("Next(log): %v", err)
	}

	trafCli, err := DialAddr(ctx, srv.Addr(), srv.Token())
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer trafCli.Close()
	ts, err := trafCli.WatchTraffic(ctx)
	if err != nil {
		t.Fatalf("WatchTraffic: %v", err)
	}
	defer ts.Close()

	f.ctrl.publishTraffic(TrafficSample{Up: 512, Down: 4096})
	var sample TrafficSample
	if err := ts.Next(ctx, &sample); err != nil {
		t.Fatalf("Next(traffic): %v", err)
	}
	if sample.Up != 512 || sample.Down != 4096 {
		t.Errorf("traffic sample = %+v", sample)
	}

	// The subscription owns its connection. A client that is following a
	// stream refuses calls with a clear message rather than silently reading
	// the next event as a response.
	if _, err := logsCli.Status(ctx); err == nil || !strings.Contains(err.Error(), "follows the") {
		t.Errorf("status on a streaming client = %v, want a \"dial a second client\" error", err)
	}
	// A separate client answers normally, which is what the TUI does.
	if st, err := cli2.Status(ctx); err != nil {
		t.Fatalf("status on a second client: %v", err)
	} else if !st.EngineRunning {
		t.Error("engine not running while streaming")
	}
}

// A Client is shared by the TUI's periodic status poll and its one-off action
// calls, so concurrent calls must be safe. They are serialised on the
// connection; without that, two goroutines interleave frames and the JSON
// decoder panics ("data changing underfoot").
func TestClientSupportsConcurrentCalls(t *testing.T) {
	f, _, cli := serveFixture(t)
	if err := f.ctrl.StartEngine(); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				_, err := cli.Status(ctx)
				errs <- err
			case 1:
				_, err := cli.Servers(ctx)
				errs <- err
			case 2:
				_, err := cli.Ping(ctx)
				errs <- err
			case 3:
				_, err := cli.Latency(ctx, "Node-B")
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent call failed: %v", err)
		}
	}
}

// A slow engine operation must not block other clients: the daemon has to stay
// responsive while a server switch restarts sing-box.
func TestServerStaysResponsiveDuringARestart(t *testing.T) {
	f := newFixture(t)
	f.engine.mu.Lock()
	f.engine.startDelay = 750 * time.Millisecond
	f.engine.mu.Unlock()

	srv, err := NewServer(ServerOptions{Handler: f.ctrl, RunDir: f.runDir})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slow, err := Dial(ctx, f.runDir)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer slow.Close()
	fast, err := Dial(ctx, f.runDir)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer fast.Close()

	if _, err := slow.StartEngine(ctx); err != nil {
		t.Fatalf("StartEngine: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := slow.Select(ctx, "Node-B"); err != nil {
			t.Errorf("Select: %v", err)
		}
	}()

	// While the restart is in flight, a status call must answer promptly.
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	if _, err := fast.Status(ctx); err != nil {
		t.Fatalf("status during restart: %v", err)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("status took %s during a restart; the daemon is not responsive", took)
	}
	<-done
}
