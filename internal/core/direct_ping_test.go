package core

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
)

func testPath(v6 bool) directPath {
	src := "192.0.2.1"
	if v6 {
		src = "2001:db8::1"
	}
	return directPath{Name: "physical", Index: 7, Source: netip.MustParseAddr(src)}
}
func TestDirectRouteSelection(t *testing.T) {
	p := testPath(false)
	other := p
	other.Name = "wifi"
	other.Index = 9
	routes := []directRoute{{netip.MustParsePrefix("0.0.0.0/0"), p, 100}, {netip.MustParsePrefix("0.0.0.0/0"), other, 200}}
	dst := netip.MustParseAddr("203.0.113.1")
	got, err := chooseDirectRoute(routes, "", dst)
	if err != nil || got.Name != "physical" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = chooseDirectRoute(routes, "wifi", dst)
	if err != nil || got.Name != "wifi" {
		t.Fatalf("explicit: %+v %v", got, err)
	}
	if _, err := chooseDirectRoute(routes, "tun0", dst); err == nil {
		t.Fatal("unknown/TUN interface fell back")
	}
	routes[1].Metric = 100
	if _, err := chooseDirectRoute(routes, "", dst); err == nil {
		t.Fatal("ambiguous physical default accepted")
	}
	// More specific physical routes take precedence over metric.
	routes[1].Prefix = netip.MustParsePrefix("203.0.113.0/24")
	got, err = chooseDirectRoute(routes, "", dst)
	if err != nil || got.Name != "wifi" {
		t.Fatalf("specific route: %+v %v", got, err)
	}
	if _, err := chooseDirectRoute(routes, "", netip.MustParseAddr("2001:db8::2")); err == nil {
		t.Fatal("IPv6 fell back to IPv4")
	}
}

type echoSocket struct {
	dst    netip.Addr
	writes int
	drop   bool
	block  bool
	packet []byte
	closed chan struct{}
	once   sync.Once
}

func (s *echoSocket) ReadFrom(p []byte) (int, net.Addr, error) {
	if s.block {
		<-s.closed
		return 0, nil, net.ErrClosed
	}
	if s.drop {
		return 0, nil, &net.OpError{Op: "read", Err: timeoutErr{}}
	}
	copy(p, s.packet)
	return len(s.packet), &net.IPAddr{IP: net.IP(s.dst.AsSlice())}, nil
}
func (s *echoSocket) WriteTo(p []byte, _ net.Addr) (int, error) {
	s.writes++
	s.packet = append([]byte(nil), p...)
	s.packet[0] = 0
	if s.dst.Is6() {
		s.packet[0] = 129
	}
	return len(p), nil
}
func (s *echoSocket) Close() error                     { s.once.Do(func() { close(s.closed) }); return nil }
func (s *echoSocket) LocalAddr() net.Addr              { return &net.IPAddr{} }
func (s *echoSocket) SetDeadline(time.Time) error      { return nil }
func (s *echoSocket) SetReadDeadline(time.Time) error  { return nil }
func (s *echoSocket) SetWriteDeadline(time.Time) error { return nil }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func echoDeps(ip netip.Addr, sock *echoSocket) pingDeps {
	return pingDeps{
		route: func(iface string, dst netip.Addr) (directPath, error) { return testPath(dst.Is6()), nil },
		listen: func(ctx context.Context, path directPath, dst netip.Addr) (net.PacketConn, error) {
			if path.Index != 7 || path.Name != "physical" || dst != ip {
				return nil, errors.New("unbound/wrong target")
			}
			return sock, nil
		},
		lookup: func(context.Context, config.ResolverAddress, netip.Addr, string, string) ([]netip.Addr, error) {
			panic("literal IP attempted DNS")
		},
	}
}
func TestDirectICMPEchoIPv4IPv6(t *testing.T) {
	for _, addr := range []string{"203.0.113.8", "2001:db8::8"} {
		t.Run(addr, func(t *testing.T) {
			ip := netip.MustParseAddr(addr)
			sock := &echoSocket{dst: ip, closed: make(chan struct{})}
			res, err := directPing(context.Background(), PingRequest{Host: addr}, echoDeps(ip, sock))
			if err != nil || res.Sent != 3 || res.Received != 3 || res.PacketLoss != 0 || res.Ms < 1 || res.Address != addr || res.Interface != "physical" {
				t.Fatalf("%+v %v", res, err)
			}
			select {
			case <-sock.closed:
			default:
				t.Fatal("socket leaked")
			}
		})
	}
}
func TestDirectICMPNoReplyAndBindingFailure(t *testing.T) {
	ip := netip.MustParseAddr("203.0.113.8")
	sock := &echoSocket{dst: ip, drop: true, closed: make(chan struct{})}
	res, err := directPing(context.Background(), PingRequest{Host: ip.String()}, echoDeps(ip, sock))
	if err == nil || !strings.Contains(err.Error(), "no ICMP reply") || res.PacketLoss != 100 || res.Sent != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	deps := echoDeps(ip, sock)
	calls := 0
	deps.listen = func(context.Context, directPath, netip.Addr) (net.PacketConn, error) {
		calls++
		return nil, errors.New("bind denied")
	}
	_, err = directPing(context.Background(), PingRequest{Host: ip.String()}, deps)
	if err == nil || !strings.Contains(err.Error(), "bind denied") || calls != 1 {
		t.Fatalf("binding fallback: %v calls=%d", err, calls)
	}
	deps.route = func(string, netip.Addr) (directPath, error) { return directPath{}, errors.New("no physical route") }
	_, err = directPing(context.Background(), PingRequest{Host: ip.String()}, deps)
	if err == nil || calls != 1 {
		t.Fatalf("route fallback: %v calls=%d", err, calls)
	}
}
func TestDirectICMPCancelAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "timeout"}[timeout], func(t *testing.T) {
			ip := netip.MustParseAddr("203.0.113.8")
			sock := &echoSocket{dst: ip, block: true, closed: make(chan struct{})}
			deps := echoDeps(ip, sock)
			entered := make(chan struct{})
			deps.listen = func(context.Context, directPath, netip.Addr) (net.PacketConn, error) {
				close(entered)
				return sock, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := directPing(ctx, PingRequest{Host: ip.String()}, deps); done <- err }()
			<-entered
			if !timeout {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if timeout {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("%v want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel did not interrupt socket")
			}
		})
	}
}
func TestEchoReplyMatchesTargetTokenAndSequence(t *testing.T) {
	dst := netip.MustParseAddr("203.0.113.8")
	payload := []byte("unique-token")
	p := echoRequest(dst, testPath(false).Source, 5, 2, payload)
	p[0] = 0
	from := &net.IPAddr{IP: net.ParseIP(dst.String())}
	if !echoReply(p, from, dst, 5, 2, payload) {
		t.Fatal("valid reply rejected")
	}
	if echoReply(p, from, dst, 5, 3, payload) || echoReply(p, from, dst, 6, 2, payload) {
		t.Fatal("foreign echo accepted")
	}
	p[8] ^= 1
	if echoReply(p, from, dst, 5, 2, payload) {
		t.Fatal("foreign payload accepted")
	}
	p[8] ^= 1
	header := make([]byte, 20)
	header[0] = 0x45
	header[9] = 1
	if !echoReply(append(header, p...), from, dst, 5, 2, payload) {
		t.Fatal("Windows IPv4 header rejected")
	}
	// Verify checksum generation in both families without relying on the OS.
	req := echoRequest(dst, testPath(false).Source, 5, 2, payload)
	if internetChecksum(req) != 0 {
		t.Fatal("IPv4 checksum")
	}
	dst = netip.MustParseAddr("2001:db8::8")
	src := testPath(true).Source
	req = echoRequest(dst, src, 5, 2, payload)
	pseudo := make([]byte, 40)
	copy(pseudo[:16], src.AsSlice())
	copy(pseudo[16:32], dst.AsSlice())
	binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(req)))
	pseudo[39] = 58
	if internetChecksum(append(pseudo, req...)) != 0 {
		t.Fatal("IPv6 checksum")
	}
}
func TestDirectProfileDNS(t *testing.T) {
	policy := &config.ProfileDNS{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: "tcp://resolver.example:5353"}}, Nameservers: []string{"udp://192.0.2.53:53"}}
	var calls []string
	lookup := func(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host, iface string) ([]netip.Addr, error) {
		if iface != "physical" {
			t.Fatal("DNS lost interface")
		}
		calls = append(calls, ra.Type+":"+endpoint.String()+":"+host)
		if host == "resolver.example" {
			return []netip.Addr{netip.MustParseAddr("2001:db8::53")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("2001:db8::8"), netip.MustParseAddr("203.0.113.8")}, nil
	}
	ips, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy, Interface: "physical"}, lookup)
	if err != nil || len(ips) != 2 || len(calls) != 2 || calls[0] != "udp:192.0.2.53:resolver.example" || calls[1] != "tcp:2001:db8::53:node.example" {
		t.Fatalf("%v %v %v", ips, calls, err)
	}
	for _, tc := range []struct {
		name string
		dns  *config.ProfileDNS
	}{
		{"unsupported", &config.ProfileDNS{Nameservers: []string{"https://dns.example/dns-query"}}},
		{"no bootstrap", &config.ProfileDNS{Nameservers: []string{"tcp://resolver.example:53"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: tc.dns}, lookup); err == nil {
				t.Fatal("unsafe default DNS fallback")
			}
		})
	}
	fake := func(context.Context, config.ResolverAddress, netip.Addr, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.18.1.1")}, nil
	}
	_, err = resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: &config.ProfileDNS{Nameservers: []string{"192.0.2.53"}}}, fake)
	if err == nil {
		t.Fatal("FakeIP answer accepted")
	}
	for _, host := range []string{"127.0.0.1", "198.18.0.2", "fc00::1", "::1"} {
		if _, err := resolvePingHost(context.Background(), PingRequest{Host: host}, lookup); err == nil {
			t.Fatalf("unsafe literal %s", host)
		}
	}
}
