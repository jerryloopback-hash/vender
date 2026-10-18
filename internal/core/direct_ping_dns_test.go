package core

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

func dnsTestAnswer(q []byte, ip netip.Addr) []byte {
	p := append([]byte(nil), q...)
	p[2] = 0x81
	p[3] = 0x80
	p[7] = 1
	p = append(p, 0xc0, 12, q[len(q)-4], q[len(q)-3], 0, 1, 0, 0, 0, 10, 0, byte(len(ip.AsSlice())))
	return append(p, ip.AsSlice()...)
}

// net.Pipe has stream semantics. This adapter preserves one UDP write/read
// per query without ever opening a real network socket.
type dnsDatagram struct{ net.Conn }

func TestDirectDNSUDPAndTCPWire(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		for _, addr := range []string{"192.0.2.53", "2001:db8::53"} {
			t.Run(transport+addr, func(t *testing.T) {
				endpoint := netip.MustParseAddr(addr)
				path := testPath(endpoint.Is6())
				var wg sync.WaitGroup
				defer wg.Wait()
				dial := func(ctx context.Context, network, address string, p directPath) (net.Conn, error) {
					family := "4"
					if endpoint.Is6() {
						family = "6"
					}
					if network != transport+family || address != net.JoinHostPort(addr, "5353") || p.Index != 7 {
						t.Errorf("unconstrained DNS: %s %s %+v", network, address, p)
					}
					client, server := net.Pipe()
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer server.Close()
						var q []byte
						if transport == "tcp" {
							var h [2]byte
							if _, err := io.ReadFull(server, h[:]); err != nil {
								return
							}
							q = make([]byte, int(binary.BigEndian.Uint16(h[:])))
							if _, err := io.ReadFull(server, q); err != nil {
								return
							}
						} else {
							buf := make([]byte, 1024)
							n, err := server.Read(buf)
							if err != nil {
								return
							}
							q = buf[:n]
						}
						typ := binary.BigEndian.Uint16(q[len(q)-4 : len(q)-2])
						ip := netip.MustParseAddr("203.0.113.9")
						if typ == 28 {
							ip = netip.MustParseAddr("2001:db8::9")
						}
						answer := dnsTestAnswer(q, ip)
						if transport == "tcp" {
							h := make([]byte, 2)
							binary.BigEndian.PutUint16(h, uint16(len(answer)))
							answer = append(h, answer...)
						}
						_, _ = server.Write(answer)
					}()
					return client, nil
				}
				ips, err := lookupDNSWithDial(context.Background(), config.ResolverAddress{Type: transport, Port: 5353}, endpoint, "node.example", path, dial)
				if err != nil || len(ips) != 2 || ips[0].String() != "203.0.113.9" || ips[1].String() != "2001:db8::9" {
					t.Fatalf("%v %v", ips, err)
				}
			})
		}
	}
}
func TestDirectDNSCancellationAndBindFailure(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.53")
	path := testPath(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	serverDone := make(chan struct{})
	dial := func(context.Context, string, string, directPath) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer close(serverDone)
			defer server.Close()
			close(started)
			_, _ = io.Copy(io.Discard, server)
		}()
		return client, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := lookupDNSWithDial(ctx, config.ResolverAddress{Type: "udp", Port: 53}, endpoint, "node.example", path, dial)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DNS cancellation stuck")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("DNS socket not closed")
	}
	calls := 0
	fail := func(context.Context, string, string, directPath) (net.Conn, error) {
		calls++
		return nil, errors.New("bind denied")
	}
	_, err := lookupDNSWithDial(context.Background(), config.ResolverAddress{Type: "tcp", Port: 53}, endpoint, "node.example", path, fail)
	if err == nil || !strings.Contains(err.Error(), "bind denied") || calls != 2 {
		t.Fatalf("DNS fallback: calls=%d %v", calls, err)
	}
}
func TestDirectDNSTruncationRetriesSameEndpointWithTCP(t *testing.T) {
	endpoint := netip.MustParseAddr("192.0.2.53")
	var networks []string
	var wg sync.WaitGroup
	defer wg.Wait()
	dial := func(_ context.Context, network, address string, path directPath) (net.Conn, error) {
		if address != "192.0.2.53:53" || path.Index != 7 {
			t.Error("TCP fallback lost endpoint/interface")
		}
		networks = append(networks, network)
		client, server := net.Pipe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer server.Close()
			var q []byte
			if network == "udp4" {
				buf := make([]byte, 1024)
				n, _ := server.Read(buf)
				q = buf[:n]
			} else {
				var h [2]byte
				_, _ = io.ReadFull(server, h[:])
				q = make([]byte, int(binary.BigEndian.Uint16(h[:])))
				_, _ = io.ReadFull(server, q)
			}
			if len(q) < 12 {
				return
			}
			if network == "udp4" {
				reply := append([]byte(nil), q...)
				reply[2] = 0x83
				_, _ = server.Write(reply)
				return
			}
			ip := netip.MustParseAddr("203.0.113.9")
			if binary.BigEndian.Uint16(q[len(q)-4:len(q)-2]) == 28 {
				ip = netip.MustParseAddr("2001:db8::9")
			}
			reply := dnsTestAnswer(q, ip)
			h := make([]byte, 2)
			binary.BigEndian.PutUint16(h, uint16(len(reply)))
			_, _ = server.Write(append(h, reply...))
		}()
		return client, nil
	}
	ips, err := lookupDNSWithDial(context.Background(), config.ResolverAddress{Type: "udp", Port: 53}, endpoint, "node.example", testPath(false), dial)
	if err != nil || len(ips) != 2 || strings.Join(networks, ",") != "udp4,tcp4,udp4,tcp4" {
		t.Fatalf("%v %v %v", networks, ips, err)
	}
}
func TestDNSRejectsForeignAndMalformedAnswers(t *testing.T) {
	q, err := dnsQuestion("node.example", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := dnsTestAnswer(q, netip.MustParseAddr("203.0.113.9"))
	for _, change := range []func([]byte){
		func(p []byte) { p[0] ^= 1 },
		func(p []byte) { p[12] = 0xc0; p[13] = 12 }, // compression loop
		func(p []byte) { p[13] = 'X' },
		func(p []byte) { p[len(p)-5] = 99 }, // impossible RDLENGTH
	} {
		copyP := append([]byte(nil), p...)
		change(copyP)
		if _, _, _, err := parseDNSAnswer(copyP, q, "node.example", 1); err == nil {
			t.Fatal("invalid/foreign answer accepted")
		}
	}
	for _, host := range []string{"", "x..example", strings.Repeat("a", 64) + ".example", "中文.example"} {
		if _, err := dnsQuestion(host, 1); err == nil {
			t.Fatalf("bad hostname accepted: %s", host)
		}
	}
}

func TestDirectDNSCNAMEAndUnrelatedRecords(t *testing.T) {
	q, err := dnsQuestion("node.example", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliasQuery, err := dnsQuestion("target.example", 1)
	if err != nil {
		t.Fatal(err)
	}
	alias := aliasQuery[12 : len(aliasQuery)-4]
	p := append([]byte(nil), q...)
	p[2] = 0x81
	p[3] = 0x80
	p[7] = 1
	p = append(p, 0xc0, 12, 0, 5, 0, 1, 0, 0, 0, 10, 0, byte(len(alias)))
	p = append(p, alias...)
	ips, name, tc, err := parseDNSAnswer(p, q, "node.example", 1)
	if err != nil || tc || len(ips) != 0 || name != "target.example" {
		t.Fatalf("%v %s %v %v", ips, name, tc, err)
	}
	// An additional unrelated owner must not contribute an IP result.
	p[7] = 2
	otherQuery, _ := dnsQuestion("foreign.example", 1)
	p = append(p, otherQuery[12:len(otherQuery)-4]...)
	p = append(p, 0, 1, 0, 1, 0, 0, 0, 10, 0, 4, 203, 0, 113, 99)
	ips, name, _, err = parseDNSAnswer(p, q, "node.example", 1)
	if err != nil || len(ips) != 0 || name != "target.example" {
		t.Fatalf("unrelated address accepted: %v %s %v", ips, name, err)
	}
	// A compression loop must fail without hanging.
	loop := append([]byte(nil), p...)
	loop[12] = 0xc0
	loop[13] = 12
	if _, _, _, err := parseDNSAnswer(loop, q, "node.example", 1); err == nil {
		t.Fatal("compression loop accepted")
	}
}

func TestPingHostnameUsesBuiltinDirectDNS(t *testing.T) {
	for _, policy := range []*config.ProfileDNS{nil, {}} {
		calls := 0
		lookup := func(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host, iface string) ([]netip.Addr, error) {
			calls++
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > pingDNSLookupTimeout {
				t.Fatal("unbounded default DNS lookup")
			}
			if ra.Type != "udp" || ra.Port != 53 || endpoint.String() != rules.BuiltinDirectDNSAddress || host != "manual.example" || iface != "physical" {
				t.Fatalf("wrong default/path: %+v %s %s %s", ra, endpoint, host, iface)
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
		}
		ips, err := resolvePingHost(context.Background(), PingRequest{Host: "manual.example", DNS: policy, Interface: "physical"}, lookup)
		if err != nil || len(ips) != 1 || calls != 1 {
			t.Fatalf("manual hostname: %v %v calls=%d", ips, err, calls)
		}
	}
	// Exercise the whole probe path: a manual hostname reaches the same ICMP
	// implementation after the built-in resolver, without opening real sockets.
	ip := netip.MustParseAddr("203.0.113.9")
	sock := &echoSocket{dst: ip, closed: make(chan struct{})}
	deps := echoDeps(ip, sock)
	deps.lookup = func(context.Context, config.ResolverAddress, netip.Addr, string, string) ([]netip.Addr, error) {
		return []netip.Addr{ip}, nil
	}
	result, err := directPing(context.Background(), PingRequest{Host: "manual.example", Interface: "physical"}, deps)
	if err != nil || result.Address != ip.String() || result.Received != 3 {
		t.Fatalf("hostname ICMP: %+v %v", result, err)
	}
}

func TestPingNameserversOrderedFailover(t *testing.T) {
	policy := &config.ProfileDNS{
		NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "other.example", Server: "192.0.2.99"}},
		Nameservers:     []string{"udp://192.0.2.53", "tcp://[2001:db8::53]:5353", "192.0.2.55"},
	}
	for _, firstErr := range []error{errors.New("first resolver unavailable"), context.DeadlineExceeded, nil} {
		var calls []string
		lookup := func(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host, iface string) ([]netip.Addr, error) {
			if host != "node.example" || iface != "physical" {
				t.Fatal("candidate lost hostname/interface")
			}
			calls = append(calls, endpoint.String())
			switch endpoint.String() {
			case "192.0.2.53":
				return nil, firstErr // Includes empty/nil-error responses.
			case "2001:db8::53":
				if ra.Type != "tcp" || ra.Port != 5353 {
					t.Fatal("TCP candidate changed")
				}
				return []netip.Addr{netip.MustParseAddr("2001:db8::9")}, nil
			default:
				t.Fatal("queried a later/conflicting resolver after success")
				return nil, nil
			}
		}
		ips, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy, Interface: "physical"}, lookup)
		if err != nil || len(ips) != 1 || ips[0].String() != "2001:db8::9" || strings.Join(calls, ",") != "192.0.2.53,2001:db8::53" {
			t.Fatalf("ordered candidates: %v %v %v", ips, calls, err)
		}
	}
	// A successful first answer is authoritative for this measurement; never
	// merge or compare a contradictory answer from a later nameserver.
	var calls []string
	lookup := func(_ context.Context, _ config.ResolverAddress, endpoint netip.Addr, _, _ string) ([]netip.Addr, error) {
		calls = append(calls, endpoint.String())
		return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
	}
	ips, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy}, lookup)
	if err != nil || len(ips) != 1 || len(calls) != 1 || calls[0] != "192.0.2.53" {
		t.Fatalf("merged candidates: %v %v %v", ips, calls, err)
	}
}

func TestPingBootstrapEmptyCandidateFailsOver(t *testing.T) {
	policy := &config.ProfileDNS{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: "tcp://resolver.example:5353"}}, Nameservers: []string{"192.0.2.53", "192.0.2.54"}}
	var calls []string
	lookup := func(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host, iface string) ([]netip.Addr, error) {
		if iface != "physical" {
			t.Fatal("bootstrap lost physical interface")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > pingDNSLookupTimeout {
			t.Fatal("unbounded bootstrap lookup")
		}
		calls = append(calls, host+"@"+endpoint.String())
		if host == "resolver.example" {
			if endpoint.String() == "192.0.2.53" {
				return nil, nil
			}
			return []netip.Addr{netip.MustParseAddr("2001:db8::53")}, nil
		}
		if ra.Type != "tcp" || ra.Port != 5353 || endpoint.String() != "2001:db8::53" {
			t.Fatal("provider resolver changed after bootstrap")
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
	}
	ips, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy, Interface: "physical"}, lookup)
	if err != nil || len(ips) != 1 || strings.Join(calls, ",") != "resolver.example@192.0.2.53,resolver.example@192.0.2.54,node.example@2001:db8::53" {
		t.Fatalf("%v %v %v", ips, calls, err)
	}
}

func TestPingExclusiveProfileNeverUsesDefault(t *testing.T) {
	for _, resolver := range []string{"tcp://192.0.2.99:5353", "tcp://resolver.example:5353", "https://resolver.example/dns-query", "tls://192.0.2.99"} {
		t.Run(resolver, func(t *testing.T) {
			policy := &config.ProfileDNS{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: resolver}}, Nameservers: []string{"192.0.2.53", "192.0.2.54"}}
			var calls []string
			lookup := func(_ context.Context, _ config.ResolverAddress, endpoint netip.Addr, host, iface string) ([]netip.Addr, error) {
				if endpoint.String() == rules.BuiltinDirectDNSAddress {
					t.Fatal("provider policy fell back to built-in DNS")
				}
				if iface != "physical" {
					t.Fatal("profile DNS lost interface")
				}
				calls = append(calls, host+"@"+endpoint.String())
				if host == "resolver.example" {
					if endpoint.String() == "192.0.2.53" {
						return nil, errors.New("first bootstrap failed")
					}
					return []netip.Addr{netip.MustParseAddr("192.0.2.99")}, nil
				}
				if endpoint.String() != "192.0.2.99" {
					t.Fatal("exclusive node query used nameserver/bootstrap directly")
				}
				return nil, errors.New("provider resolver failed")
			}
			ips, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy, Interface: "physical"}, lookup)
			if err == nil || len(ips) != 0 {
				t.Fatalf("%v %v", ips, err)
			}
			if strings.HasPrefix(resolver, "https:") || strings.HasPrefix(resolver, "tls:") {
				if len(calls) != 0 || !strings.Contains(err.Error(), "unsupported") {
					t.Fatalf("unsupported policy downgraded: %v %v", calls, err)
				}
			} else if len(calls) == 0 {
				t.Fatal("provider resolver was not attempted")
			}
		})
	}
	// Exhausting an unmatched profile's candidates also must not reach default.
	policy := &config.ProfileDNS{Nameservers: []string{"192.0.2.53", "192.0.2.54"}}
	calls := 0
	_, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy}, func(_ context.Context, _ config.ResolverAddress, endpoint netip.Addr, _, _ string) ([]netip.Addr, error) {
		calls++
		if endpoint.String() == rules.BuiltinDirectDNSAddress {
			t.Fatal("exhausted candidates used default")
		}
		return nil, errors.New("unavailable")
	})
	if err == nil || calls != 2 {
		t.Fatalf("exhausted profile: %d %v", calls, err)
	}
}

func TestPingEmptyDNSAndBootstrapResultsAlwaysError(t *testing.T) {
	policies := []*config.ProfileDNS{
		nil, {},
		{Nameservers: []string{"192.0.2.53", "192.0.2.54"}},
		{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: "192.0.2.99"}}},
		{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: "resolver.example"}}, Nameservers: []string{"192.0.2.53", "192.0.2.54"}},
		{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: "resolver.example"}}},
	}
	for i, policy := range policies {
		for _, result := range [][]netip.Addr{nil, {}, {{}}, {netip.MustParseAddr("198.18.1.1")}} {
			ips, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy}, func(context.Context, config.ResolverAddress, netip.Addr, string, string) ([]netip.Addr, error) {
				return result, nil
			})
			if err == nil || len(ips) != 0 {
				t.Fatalf("policy %d results %v: ips=%v error=%v", i, result, ips, err)
			}
		}
	}
	// Previously an errored bootstrap response could leave stale endpoints in
	// the accumulator; no endpoint from a failed lookup may be used.
	policy := &config.ProfileDNS{NamespacePolicy: []config.DNSNamespacePolicy{{Domain: "node.example", Server: "resolver.example"}}, Nameservers: []string{"192.0.2.53", "192.0.2.54"}}
	_, err := resolvePingHost(context.Background(), PingRequest{Host: "node.example", DNS: policy}, func(_ context.Context, _ config.ResolverAddress, endpoint netip.Addr, host, _ string) ([]netip.Addr, error) {
		if host != "resolver.example" {
			t.Fatal("used endpoints returned alongside a bootstrap error")
		}
		if endpoint.String() == "192.0.2.53" {
			return []netip.Addr{netip.MustParseAddr("192.0.2.99")}, errors.New("bootstrap failed")
		}
		return nil, nil
	})
	if err == nil {
		t.Fatal("empty bootstrap returned nil error")
	}
}

func TestPingDNSAndBootstrapDeadlines(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		policy := &config.ProfileDNS{Nameservers: []string{"192.0.2.53", "192.0.2.54"}}
		if bootstrap {
			policy.NamespacePolicy = []config.DNSNamespacePolicy{{Domain: "node.example", Server: "resolver.example"}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		calls := 0
		ips, err := resolvePingHost(ctx, PingRequest{Host: "node.example", DNS: policy}, func(ctx context.Context, _ config.ResolverAddress, _ netip.Addr, _, _ string) ([]netip.Addr, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > pingDNSLookupTimeout {
				t.Error("unbounded DNS/bootstrap lookup")
			}
			<-ctx.Done()
			return nil, nil
		})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || len(ips) != 0 || calls != 1 {
			t.Fatalf("bootstrap=%v %v %v calls=%d", bootstrap, ips, err, calls)
		}
	}
}
