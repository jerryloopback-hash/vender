//go:build windows

package core

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Microsoft documents IP_UNICAST_IF as the outgoing IPv4 interface, in
// NETWORK byte order; IPV6_UNICAST_IF uses HOST byte order. Apply to every
// ICMP and DNS socket before binding/connecting. Source binding alone is not
// the routing constraint. See docs/DIRECT-PING.md for primary references.
func bindDirectSocket(path directPath, ipv6 bool, raw syscall.RawConn) error {
	if path.Index <= 0 {
		return fmt.Errorf("invalid physical interface index")
	}
	level, opt, value := windows.IPPROTO_IP, 31, path.Index // IP_UNICAST_IF
	if ipv6 {
		level, opt = windows.IPPROTO_IPV6, 31
	} else {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(path.Index))
		value = int(binary.NativeEndian.Uint32(b[:]))
	}
	var bindErr error
	err := raw.Control(func(fd uintptr) { bindErr = windows.SetsockoptInt(windows.Handle(fd), level, opt, value) })
	if err != nil {
		return err
	}
	return bindErr
}
func physicalPath(iface string, dst netip.Addr) (directPath, error) {
	family := uint16(windows.AF_INET)
	if dst.Is6() {
		family = windows.AF_INET6
	}
	var size uint32 = 15000
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(uint32(family), windows.GAA_FLAG_INCLUDE_PREFIX, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return directPath{}, err
		}
		break
	}
	paths := map[uint64]directPath{}
	metrics := map[uint64]uint32{}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		name := windows.UTF16PtrToString(a.FriendlyName)
		if iface != "" && iface != name || a.OperStatus != 1 {
			continue
		}
		row := windows.MibIfRow2{InterfaceLuid: a.Luid}
		if err := windows.GetIfEntry2Ex(0, &row); err != nil {
			continue
		}
		// HardwareInterface bit is positive evidence. An Ethernet-like TAP/Wintun
		// name or MAC is not. Also require an up Ethernet/Wi-Fi, non-tunnel device.
		if row.InterfaceAndOperStatusFlags&1 == 0 || row.TunnelType != 0 || row.OperStatus != 1 || (row.Type != 6 && row.Type != 71) {
			continue
		}
		index := a.IfIndex
		metric := a.Ipv4Metric
		if dst.Is6() {
			index = a.Ipv6IfIndex
			metric = a.Ipv6Metric
		}
		var source netip.Addr
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			if u.DadState != 4 {
				continue
			} // IpDadStatePreferred, excludes tentative/duplicate
			ip, ok := netip.AddrFromSlice(u.Address.IP())
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if ip.Is4() == dst.Is4() && safePingIP(ip) {
				source = ip
				break
			}
		}
		if index > 0 && source.IsValid() {
			paths[a.Luid] = directPath{Name: name, Index: int(index), Source: source}
			metrics[a.Luid] = metric
		}
	}
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(family, &table); err != nil {
		return directPath{}, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var routes []directRoute
	for _, r := range table.Rows() {
		path, ok := paths[r.InterfaceLuid]
		if !ok || r.Loopback != 0 || r.ValidLifetime == 0 {
			continue
		}
		ip := windowsRouteIP(&r.DestinationPrefix.Prefix)
		p := netip.PrefixFrom(ip, int(r.DestinationPrefix.PrefixLength))
		if !p.IsValid() {
			continue
		}
		routes = append(routes, directRoute{Prefix: p, Path: path, Metric: uint64(r.Metric) + uint64(metrics[r.InterfaceLuid])})
	}
	return chooseDirectRoute(routes, iface, dst)
}
func windowsRouteIP(raw *windows.RawSockaddrInet) netip.Addr {
	switch raw.Family {
	case windows.AF_INET:
		return netip.AddrFrom4((*windows.RawSockaddrInet4)(unsafe.Pointer(raw)).Addr)
	case windows.AF_INET6:
		return netip.AddrFrom16((*windows.RawSockaddrInet6)(unsafe.Pointer(raw)).Addr)
	}
	return netip.Addr{}
}
