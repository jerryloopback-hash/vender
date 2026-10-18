package core

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ll-lite/vender/internal/config"
)

type directDNSDial func(context.Context, string, string, directPath) (net.Conn, error)

func lookupBoundDNS(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host string, path directPath, bind func(directPath, bool, syscall.RawConn) error) ([]netip.Addr, error) {
	return lookupDNSWithDial(ctx, ra, endpoint, host, path, func(ctx context.Context, network, address string, path directPath) (net.Conn, error) {
		d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error { return bind(path, endpoint.Is6(), c) }}
		src := net.IP(path.Source.AsSlice())
		if strings.HasPrefix(network, "tcp") {
			d.LocalAddr = &net.TCPAddr{IP: src}
		} else {
			d.LocalAddr = &net.UDPAddr{IP: src}
		}
		return d.DialContext(ctx, network, address)
	})
}

// Query the exact hostname at the selected profile or built-in endpoint. No
// net.Resolver, NSS, hosts file, resolv.conf search suffix or system DNS.
func lookupDNSWithDial(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host string, path directPath, dial directDNSDial) ([]netip.Addr, error) {
	var ips []netip.Addr
	var last error
	for _, typ := range []uint16{1, 28} {
		found, err := queryDirectDNS(ctx, ra, endpoint, host, typ, path, dial, 0)
		if err != nil {
			last = err
		} else {
			ips = append(ips, found...)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if len(ips) > 0 {
		return ips, nil
	}
	if last != nil {
		return nil, last
	}
	return nil, errors.New("profile DNS returned no A/AAAA addresses")
}
func queryDirectDNS(ctx context.Context, ra config.ResolverAddress, endpoint netip.Addr, host string, typ uint16, path directPath, dial directDNSDial, depth int) ([]netip.Addr, error) {
	if depth > 8 {
		return nil, errors.New("profile DNS CNAME chain too long")
	}
	query, err := dnsQuestion(host, typ)
	if err != nil {
		return nil, err
	}
	transport := ra.Type
	if transport != "udp" && transport != "tcp" {
		return nil, errors.New("direct DNS requires UDP/TCP")
	}
	for {
		reply, err := exchangeDirectDNS(ctx, transport, endpoint, ra.Port, query, path, dial)
		if err != nil {
			return nil, err
		}
		ips, alias, truncated, err := parseDNSAnswer(reply, query, host, typ)
		if err != nil {
			return nil, err
		}
		if truncated {
			if transport == "tcp" {
				return nil, errors.New("truncated TCP DNS response")
			}
			transport = "tcp"
			continue
		}
		if len(ips) > 0 || alias == "" {
			return ips, nil
		}
		return queryDirectDNS(ctx, ra, endpoint, alias, typ, path, dial, depth+1)
	}
}
func exchangeDirectDNS(ctx context.Context, transport string, endpoint netip.Addr, port int, query []byte, path directPath, dial directDNSDial) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	family := "4"
	if endpoint.Is6() {
		family = "6"
	}
	conn, err := dial(ctx, transport+family, net.JoinHostPort(endpoint.String(), strconv.Itoa(port)), path)
	if err != nil {
		return nil, fmt.Errorf("profile DNS interface/dial: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	wire := query
	if transport == "tcp" {
		wire = make([]byte, 2, len(query)+2)
		binary.BigEndian.PutUint16(wire, uint16(len(query)))
		wire = append(wire, query...)
	}
	if _, err := io.Copy(conn, strings.NewReader(string(wire))); err != nil {
		return nil, err
	}
	if transport == "tcp" {
		var header [2]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return nil, err
		}
		size := int(binary.BigEndian.Uint16(header[:]))
		if size < 12 {
			return nil, errors.New("short TCP DNS response")
		}
		reply := make([]byte, size)
		_, err := io.ReadFull(conn, reply)
		return reply, err
	}
	reply := make([]byte, 4096)
	n, err := conn.Read(reply)
	return reply[:n], err
}
func dnsQuestion(host string, typ uint16) ([]byte, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if len(host) == 0 || len(host) > 253 {
		return nil, errors.New("invalid DNS hostname length")
	}
	p := make([]byte, 12)
	if _, err := rand.Read(p[:2]); err != nil {
		return nil, err
	}
	p[2] = 1
	p[5] = 1 // recursion desired, one question
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, errors.New("invalid DNS label length")
		}
		for _, c := range []byte(label) {
			if c <= 32 || c >= 127 {
				return nil, errors.New("direct DNS requires an ASCII/punycode hostname")
			}
		}
		p = append(p, byte(len(label)))
		p = append(p, label...)
	}
	p = append(p, 0, byte(typ>>8), byte(typ), 0, 1)
	return p, nil
}
func dnsName(p []byte, offset int) (string, int, error) {
	end := -1
	var labels []string
	for steps := 0; steps < 128; steps++ {
		if offset < 0 || offset >= len(p) {
			break
		}
		n := int(p[offset])
		offset++
		if n == 0 {
			if end < 0 {
				end = offset
			}
			return strings.ToLower(strings.Join(labels, ".")), end, nil
		}
		if n&0xc0 == 0xc0 {
			if offset >= len(p) {
				break
			}
			if end < 0 {
				end = offset + 1
			}
			offset = (n&0x3f)<<8 | int(p[offset])
			continue
		}
		if n > 63 || offset+n > len(p) {
			break
		}
		labels = append(labels, string(p[offset:offset+n]))
		offset += n
	}
	return "", 0, errors.New("malformed/compression-loop DNS name")
}
func parseDNSAnswer(p, query []byte, host string, typ uint16) ([]netip.Addr, string, bool, error) {
	bad := func() ([]netip.Addr, string, bool, error) {
		return nil, "", false, errors.New("invalid profile DNS response")
	}
	if len(p) < 12 || len(query) < 12 || binary.BigEndian.Uint16(p) != binary.BigEndian.Uint16(query) || p[2]&0x80 == 0 || p[2]&0x78 != 0 {
		return bad()
	}
	if p[3]&15 != 0 {
		return nil, "", false, fmt.Errorf("profile DNS response code %d", p[3]&15)
	}
	if p[2]&2 != 0 {
		return nil, "", true, nil
	}
	if binary.BigEndian.Uint16(p[4:6]) != 1 {
		return bad()
	}
	name, off, err := dnsName(p, 12)
	if err != nil || name != strings.TrimSuffix(strings.ToLower(host), ".") || off+4 > len(p) || binary.BigEndian.Uint16(p[off:off+2]) != typ || binary.BigEndian.Uint16(p[off+2:off+4]) != 1 {
		return bad()
	}
	off += 4
	type record struct {
		name, alias string
		ip          netip.Addr
	}
	var records []record
	for count := int(binary.BigEndian.Uint16(p[6:8])); count > 0; count-- {
		owner, next, err := dnsName(p, off)
		if err != nil || next+10 > len(p) {
			return bad()
		}
		rt := binary.BigEndian.Uint16(p[next : next+2])
		class := binary.BigEndian.Uint16(p[next+2 : next+4])
		size := int(binary.BigEndian.Uint16(p[next+8 : next+10]))
		off = next + 10
		if off+size > len(p) {
			return bad()
		}
		r := record{name: owner}
		if class == 1 {
			switch {
			case rt == 5:
				alias, end, err := dnsName(p, off)
				if err != nil || end != off+size {
					return bad()
				}
				r.alias = alias
			case rt == typ && (rt == 1 && size == 4 || rt == 28 && size == 16):
				r.ip, _ = netip.AddrFromSlice(p[off : off+size])
			}
			records = append(records, r)
		}
		off += size
	}
	current := name
	for steps := 0; steps <= 8; steps++ {
		var ips []netip.Addr
		for _, r := range records {
			if r.name == current && r.ip.IsValid() {
				ips = append(ips, r.ip)
			}
		}
		if len(ips) > 0 {
			return ips, "", false, nil
		}
		alias := ""
		for _, r := range records {
			if r.name == current && r.alias != "" {
				alias = r.alias
				break
			}
		}
		if alias == "" {
			if current != name {
				return nil, current, false, nil
			}
			return nil, "", false, nil
		}
		current = alias
	}
	return nil, "", false, errors.New("profile DNS CNAME loop")
}
