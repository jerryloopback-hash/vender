package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
)

func TestLatencyCancellationReachesDaemonOverPipe(t *testing.T) {
	f := newFixture(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	f.ctrl.probe = func(ctx context.Context, _ core.PingRequest) (core.PingResult, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return core.PingResult{}, ctx.Err()
	}
	clientConn, serverConn := net.Pipe()
	s := &Server{handler: f.ctrl, token: "test", done: make(chan struct{})}
	served := make(chan struct{})
	go func() { s.serveConn(serverConn); close(served) }()
	cli := &Client{conn: clientConn, enc: json.NewEncoder(clientConn), dec: json.NewDecoder(bufio.NewReader(clientConn)), token: "test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cli.Latency(ctx, "Node-B"); done <- err }()
	<-entered
	cancel()
	for _, ch := range []<-chan struct{}{exited, served} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("daemon probe/connection reader leaked after cancellation")
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded unexpectedly")
		}
	case <-time.After(time.Second):
		t.Fatal("client cancellation stuck")
	}
}
func TestControlPipeStillSupportsRepeatedRequests(t *testing.T) {
	f := newFixture(t)
	clientConn, serverConn := net.Pipe()
	s := &Server{handler: f.ctrl, token: "test", done: make(chan struct{})}
	served := make(chan struct{})
	go func() { s.serveConn(serverConn); close(served) }()
	cli := &Client{conn: clientConn, enc: json.NewEncoder(clientConn), dec: json.NewDecoder(bufio.NewReader(clientConn)), token: "test"}
	defer func() {
		cli.Close()
		select {
		case <-served:
		case <-time.After(time.Second):
			t.Error("reader leaked")
		}
	}()
	for i := 0; i < 3; i++ {
		lat, err := cli.Latency(context.Background(), "Node-B")
		if err != nil || lat.Ms != 17 {
			t.Fatalf("%+v %v", lat, err)
		}
		if _, err := cli.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLatencyUsesNodesProfileDNSAndExplicitInterface(t *testing.T) {
	f := newFixture(t)
	ss, err := f.store.LoadServers()
	if err != nil {
		t.Fatal(err)
	}
	ss.Servers[1].Profile = "provider"
	ss.Servers[1].Address = "node.example"
	if err := f.store.SaveServers(ss); err != nil {
		t.Fatal(err)
	}
	ps := config.NewProfileSet()
	ps.Upsert(config.Profile{Name: "provider", DNS: &config.ProfileDNS{Nameservers: []string{"192.0.2.53"}}})
	if err := f.store.SaveProfiles(ps); err != nil {
		t.Fatal(err)
	}
	f.ctrl.cfg.Engine.BindInterface = "physical"
	f.ctrl.probe = func(ctx context.Context, req core.PingRequest) (core.PingResult, error) {
		if req.Host != "node.example" || req.Interface != "physical" || req.DNS == nil || req.DNS.Nameservers[0] != "192.0.2.53" {
			t.Fatalf("wrong profile/interface: %+v", req)
		}
		return core.PingResult{Ms: 19, Sent: 3, Received: 1, PacketLoss: 66}, nil
	}
	lat := f.ctrl.Latency(context.Background(), "Node-B")
	if lat.Err != "" || lat.Ms != 19 || lat.PacketLoss != 66 {
		t.Fatal(lat)
	}
}
