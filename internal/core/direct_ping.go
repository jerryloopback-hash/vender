package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

const DirectPingTimeout = 10 * time.Second
const pingSamples = 3

// PingRequest deliberately has no protocol, port, selected outbound or URL.
// DNS is the probed node's own profile policy, never the active profile's.
type PingRequest struct {
	Host      string
	Interface string
	DNS       *config.ProfileDNS
}
type PingResult struct {
	Ms         int
	Sent       int
	Received   int
	PacketLoss int
	Address    string
	Interface  string
}
type PingProbe func(context.Context, PingRequest) (PingResult, error)

type directPath struct {
	Name   string
	Index  int
	Source netip.Addr
}
type directRoute struct {
	Prefix netip.Prefix
	Path   directPath
	Metric uint64
}

// chooseDirectRoute only sees routes on verified physical, up interfaces.
// Equal-cost routes on different NICs are ambiguous: require bind_interface.
func chooseDirectRoute(routes []directRoute, iface string, dst netip.Addr) (directPath, error) {
	var best *directRoute
	ambiguous := false
	for i := range routes {
		r := &routes[i]
		if iface != "" && r.Path.Name != iface || !r.Prefix.Contains(dst) || !r.Path.Source.IsValid() {
			continue
		}
		if best == nil || r.Prefix.Bits() > best.Prefix.Bits() || r.Prefix.Bits() == best.Prefix.Bits() && r.Metric < best.Metric {
			best, ambiguous = r, false
		} else if r.Prefix.Bits() == best.Prefix.Bits() && r.Metric == best.Metric && r.Path.Index != best.Path.Index {
			ambiguous = true
		}
	}
	if best == nil {
		return directPath{}, fmt.Errorf("direct exit unavailable for %s (bind_interface=%q): no verified physical route", dst, iface)
	}
	if ambiguous {
		return directPath{}, errors.New("ambiguous physical routes: set engine.bind_interface")
	}
	return best.Path, nil
}

type pingDeps struct {
	route  func(string, netip.Addr) (directPath, error)
	listen func(context.Context, directPath, netip.Addr) (net.PacketConn, error)
	lookup func(context.Context, config.ResolverAddress, netip.Addr, string, string) ([]netip.Addr, error)
}

func systemPingDeps() pingDeps {
	return pingDeps{route: physicalPath, listen: listenDirectICMP, lookup: lookupDirectDNS}
}

// DirectPing uses raw echo sockets with mandatory per-socket interface binding.
// It never falls back to default routing or sing-box's URL tests.
func DirectPing(ctx context.Context, req PingRequest) (PingResult, error) {
	return directPing(ctx, req, systemPingDeps())
}
func directPing(ctx context.Context, req PingRequest, deps pingDeps) (out PingResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, DirectPingTimeout)
	defer cancel()
	defer func() {
		if out.Sent > 0 {
			out.PacketLoss = (out.Sent - out.Received) * 100 / out.Sent
		}
	}()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	ips, err := resolvePingHost(ctx, req, deps.lookup)
	if err != nil {
		return out, fmt.Errorf("direct DNS: %w", err)
	}
	// Use one address for this measurement, so samples/RTT never mix targets.
	// An unavailable address family may be skipped; a socket/binding failure is
	// terminal and must never trigger an unbound retry.
	var path directPath
	var ip netip.Addr
	for _, candidate := range ips {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		path, err = deps.route(req.Interface, candidate)
		if err == nil {
			ip = candidate
			break
		}
	}
	if !ip.IsValid() {
		return out, err
	}
	out.Address, out.Interface = ip.String(), path.Name
	conn, err := deps.listen(ctx, path, ip)
	if err != nil {
		return out, fmt.Errorf("ICMP permission/interface binding: %w", err)
	}
	defer conn.Close()
	// Closing a socket interrupts both read and write immediately on cancellation.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var token [18]byte
	if _, err := rand.Read(token[:]); err != nil {
		return out, err
	}
	id := binary.BigEndian.Uint16(token[:2])
	var total time.Duration
	for seq := 1; seq <= pingSamples; seq++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		packet := echoRequest(ip, path.Source, id, uint16(seq), token[2:])
		deadline := time.Now().Add(time.Second)
		if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
			deadline = end
		}
		if err := conn.SetDeadline(deadline); err != nil {
			return out, err
		}
		start := time.Now()
		if _, err := conn.WriteTo(packet, &net.IPAddr{IP: net.IP(ip.AsSlice())}); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("ICMP send on %s: %w", path.Name, err)
		}
		out.Sent++
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					break
				}
				return out, fmt.Errorf("ICMP receive: %w", err)
			}
			if echoReply(buf[:n], from, ip, id, uint16(seq), token[2:]) {
				out.Received++
				total += time.Since(start)
				break
			}
		}
	}
	out.PacketLoss = (out.Sent - out.Received) * 100 / out.Sent
	if out.Received == 0 {
		return out, errors.New("no ICMP reply (does not determine proxy protocol availability)")
	}
	out.Ms = max(1, int((total / time.Duration(out.Received)).Milliseconds()))
	return out, nil
}

func echoRequest(dst, src netip.Addr, id, seq uint16, payload []byte) []byte {
	p := make([]byte, 8+len(payload))
	p[0] = 8
	if dst.Is6() {
		p[0] = 128
	}
	binary.BigEndian.PutUint16(p[4:6], id)
	binary.BigEndian.PutUint16(p[6:8], seq)
	copy(p[8:], payload)
	sumData := p
	if dst.Is6() {
		// RFC 4443 pseudo-header. Some kernels also fill this in; the value is
		// already correct because the socket is bound to this source and NIC.
		pseudo := make([]byte, 40, 40+len(p))
		copy(pseudo[:16], src.AsSlice())
		copy(pseudo[16:32], dst.AsSlice())
		binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(p)))
		pseudo[39] = 58
		sumData = append(pseudo, p...)
	}
	binary.BigEndian.PutUint16(p[2:4], internetChecksum(sumData))
	return p
}
func internetChecksum(p []byte) uint16 {
	var sum uint32
	for len(p) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(p))
		p = p[2:]
	}
	if len(p) > 0 {
		sum += uint32(p[0]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
func echoReply(p []byte, from net.Addr, dst netip.Addr, id, seq uint16, payload []byte) bool {
	peer, ok := from.(*net.IPAddr)
	if !ok || !peer.IP.Equal(net.IP(dst.AsSlice())) {
		return false
	}
	// Windows raw IPv4 receives the IP header; Go/Linux usually removes it.
	if dst.Is4() && len(p) >= 20 && p[0]>>4 == 4 {
		h := int(p[0]&15) * 4
		if h < 20 || h > len(p) || p[9] != 1 {
			return false
		}
		p = p[h:]
	}
	typ := byte(0)
	if dst.Is6() {
		typ = 129
	}
	return len(p) == 8+len(payload) && p[0] == typ && p[1] == 0 &&
		binary.BigEndian.Uint16(p[4:6]) == id && binary.BigEndian.Uint16(p[6:8]) == seq && bytes.Equal(p[8:], payload)
}

func safePingIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip) && !netip.MustParsePrefix("fc00::/18").Contains(ip)
}

type pingDNSLookup func(context.Context, config.ResolverAddress, netip.Addr, string, string) ([]netip.Addr, error)

// The total DNS budget also applies when called independently of DirectPing.
// Each query/bootstrap lookup gets a smaller budget so ordered candidates can
// fail over without one unresponsive resolver consuming the whole node budget.
const pingDNSLookupTimeout = 3 * time.Second

func resolvePingHost(ctx context.Context, req PingRequest, lookup pingDNSLookup) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, DirectPingTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ip, err := netip.ParseAddr(req.Host); err == nil {
		ip = ip.Unmap()
		if !safePingIP(ip) {
			return nil, errors.New("non-unicast, local or FakeIP node address is not a physical Ping target")
		}
		return []netip.Addr{ip}, nil
	}
	var candidates []string
	if resolver := req.DNS.Match(req.Host); resolver != "" {
		// A matching provider policy is exclusive, even when it fails.
		candidates = []string{resolver}
	} else if req.DNS != nil && len(req.DNS.Nameservers) > 0 {
		candidates = req.DNS.Nameservers
	} else {
		// This is vender's explicit built-in default, not a failure fallback for a
		// configured provider resolver. Its socket uses the same mandatory binding.
		candidates = []string{rules.BuiltinDirectDNSAddress}
	}
	last := errors.New("DNS returned no usable address")
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ra, err := pingResolver(candidate)
		// Do not turn an explicitly unsupported transport into plaintext DNS.
		if err != nil {
			return nil, err
		}
		ips, err := resolvePingResolver(ctx, req, ra, lookup)
		if err == nil && len(ips) > 0 {
			return ips, nil
		}
		if err != nil {
			last = err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, last
}

func resolvePingResolver(ctx context.Context, req PingRequest, ra config.ResolverAddress, lookup pingDNSLookup) ([]netip.Addr, error) {
	var endpoints []netip.Addr
	if ip, err := netip.ParseAddr(ra.Host); err == nil {
		endpoints = []netip.Addr{ip.Unmap()}
	} else {
		// Bootstrap is explicitly supplied by this profile. Never use the built-in
		// default, system DNS or a recursive lookup of the resolver's own name.
		last := errors.New("no literal UDP/TCP profile nameserver for bootstrap")
		if req.DNS != nil {
			for _, addr := range req.DNS.Nameservers {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				boot, err := pingResolver(addr)
				if err != nil {
					last = err
					continue
				}
				ip, err := netip.ParseAddr(boot.Host)
				if err != nil || !safePingIP(ip) {
					continue
				}
				found, err := lookupPingAddresses(ctx, boot, ip.Unmap(), ra.Host, req.Interface, lookup)
				if err != nil {
					last = err
					continue
				}
				endpoints = found
				break
			}
		}
		if len(endpoints) == 0 {
			return nil, fmt.Errorf("profile DNS bootstrap failed: %w", last)
		}
	}
	last := errors.New("profile DNS resolver returned no usable endpoint")
	for _, endpoint := range endpoints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !safePingIP(endpoint) {
			last = errors.New("profile DNS returned a local/FakeIP resolver address")
			continue
		}
		ips, err := lookupPingAddresses(ctx, ra, endpoint, req.Host, req.Interface, lookup)
		if err == nil {
			return ips, nil
		}
		last = err
	}
	return nil, last
}

func lookupPingAddresses(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host, iface string, lookup pingDNSLookup) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, pingDNSLookupTimeout)
	defer cancel()
	ips, err := lookup(ctx, ra, endpoint, host, iface)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	var safe []netip.Addr
	for _, ip := range ips {
		if safePingIP(ip) {
			safe = append(safe, ip.Unmap())
		}
	}
	if len(safe) == 0 {
		return nil, errors.New("DNS returned no usable address (empty, local or FakeIP answers rejected)")
	}
	return safe, nil
}
func pingResolver(addr string) (config.ResolverAddress, error) {
	ra, err := config.ParseResolverAddress(addr)
	if err != nil {
		return ra, errors.New("invalid profile DNS resolver")
	}
	if ra.Type != "udp" && ra.Type != "tcp" {
		return ra, fmt.Errorf("profile DNS transport %q is unsupported for direct Ping (requires UDP/TCP)", ra.Type)
	}
	return ra, nil
}
func lookupDirectDNS(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host, iface string) ([]netip.Addr, error) {
	path, err := physicalPath(iface, endpoint)
	if err != nil {
		return nil, err
	}
	return lookupBoundDNS(ctx, ra, endpoint, host, path, bindDirectSocket)
}
func listenDirectICMP(ctx context.Context, path directPath, ip netip.Addr) (net.PacketConn, error) {
	network := "ip4:icmp"
	if ip.Is6() {
		network = "ip6:ipv6-icmp"
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error { return bindDirectSocket(path, ip.Is6(), c) }}
	return lc.ListenPacket(ctx, network, path.Source.String())
}
