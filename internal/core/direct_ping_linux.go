//go:build linux

package core

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func bindDirectSocket(path directPath, _ bool, raw syscall.RawConn) error {
	if path.Name == "" || path.Index <= 0 {
		return fmt.Errorf("invalid physical interface")
	}
	var bindErr error
	err := raw.Control(func(fd uintptr) {
		bindErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, path.Name)
	})
	if err != nil {
		return err
	}
	return bindErr
}
func physicalPath(iface string, dst netip.Addr) (directPath, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return directPath{}, err
	}
	paths := map[int]directPath{}
	for _, nic := range interfaces {
		if iface != "" && iface != nic.Name || nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		// A hardware device link is positive evidence. TUN/TAP, WireGuard, veth,
		// bridges and unverified virtual interfaces are deliberately rejected.
		if _, err := os.Stat(filepath.Join("/sys/class/net", nic.Name, "device")); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/class/net", nic.Name, "tun_flags")); err == nil {
			continue
		}
		source, err := interfaceSource(nic, dst)
		if err != nil {
			continue
		}
		paths[nic.Index] = directPath{Name: nic.Name, Index: nic.Index, Source: source}
	}
	family := unix.AF_INET
	if dst.Is6() {
		family = unix.AF_INET6
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, family)
	if err != nil {
		return directPath{}, err
	}
	messages, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return directPath{}, err
	}
	var routes []directRoute
	for _, msg := range messages {
		if msg.Header.Type != syscall.RTM_NEWROUTE || len(msg.Data) < unix.SizeofRtMsg {
			continue
		}
		// Use the main table's physical routes, not a VPN policy table. The bound
		// socket's oif constrains the subsequent kernel lookup; failure is terminal.
		if msg.Data[0] != byte(family) || msg.Data[4] != unix.RT_TABLE_MAIN || msg.Data[7] != unix.RTN_UNICAST || msg.Data[2] != 0 {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(&msg)
		if err != nil {
			continue
		}
		index := 0
		metric := uint64(0)
		prefixIP := netip.IPv4Unspecified()
		if dst.Is6() {
			prefixIP = netip.IPv6Unspecified()
		}
		multipath := false
		for _, a := range attrs {
			switch a.Attr.Type {
			case unix.RTA_OIF:
				if len(a.Value) >= 4 {
					index = int(binary.NativeEndian.Uint32(a.Value))
				}
			case unix.RTA_PRIORITY:
				if len(a.Value) >= 4 {
					metric = uint64(binary.NativeEndian.Uint32(a.Value))
				}
			case unix.RTA_DST:
				if ip, ok := netip.AddrFromSlice(a.Value); ok {
					prefixIP = ip
				}
			case unix.RTA_MULTIPATH:
				multipath = true
			}
		}
		path, ok := paths[index]
		if !ok || multipath {
			continue
		}
		prefix := netip.PrefixFrom(prefixIP, int(msg.Data[1]))
		if !prefix.IsValid() {
			continue
		}
		routes = append(routes, directRoute{Prefix: prefix, Path: path, Metric: metric})
	}
	return chooseDirectRoute(routes, iface, dst)
}
func interfaceSource(nic net.Interface, dst netip.Addr) (netip.Addr, error) {
	addrs, err := nic.Addrs()
	if err != nil {
		return netip.Addr{}, err
	}
	var source netip.Addr
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		ip := p.Addr().Unmap()
		if ip.Is4() != dst.Is4() || !safePingIP(ip) {
			continue
		}
		if p.Contains(dst) {
			return ip, nil
		}
		if !source.IsValid() {
			source = ip
		}
	}
	if !source.IsValid() {
		return source, fmt.Errorf("no usable source on %s", nic.Name)
	}
	return source, nil
}
