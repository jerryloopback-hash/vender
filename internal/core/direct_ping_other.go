//go:build !linux && !windows

package core

import (
	"errors"
	"net/netip"
	"syscall"
)

func physicalPath(string, netip.Addr) (directPath, error) {
	return directPath{}, errors.New("physical ICMP path is supported only on Linux/Windows")
}
func bindDirectSocket(directPath, bool, syscall.RawConn) error {
	return errors.New("physical socket binding unsupported on this platform")
}
