package core

import (
	"fmt"
	"strings"
)

// TUNPrivilege is the verdict of the pre-flight check for TUN mode: can the
// process that is about to start the engine create a TUN device?
//
// The check exists so that a missing capability produces one actionable
// sentence instead of the engine's own
// "configure tun interface: open tun: TUNSETIFF: operation not permitted",
// which says nothing about how to fix it.
type TUNPrivilege struct {
	// OK reports whether TUN mode can be expected to work.
	OK bool
	// Detail explains the verdict in one line ("running as root", "missing
	// CAP_NET_ADMIN", ...).
	Detail string
	// Hint is the actionable fix, empty when OK.
	Hint string
}

// Err renders a failed verdict as an error, and nil for a successful one.
func (p TUNPrivilege) Err() error {
	if p.OK {
		return nil
	}
	msg := "core: TUN mode is not permitted for this process: " + p.Detail
	if indented := indentHint(p.Hint); indented != "" {
		msg += "\n" + indented
	}
	return fmt.Errorf("%s", msg)
}

// CheckTUNPrivilege reports whether a TUN device can be created from here, and
// what to do about it when it cannot.
//
// engineBinary is the sing-box executable the engine will run: on Linux the
// capability may have been granted to that file with `setcap` instead of to the
// invoking process, and that is a legitimate configuration. It may be empty.
func CheckTUNPrivilege(engineBinary string) TUNPrivilege {
	return checkTUNPrivilege(engineBinary)
}

// tunPrivilegeNames renders "CAP_NET_ADMIN (and CAP_NET_RAW)" the same way in
// every platform message.
const tunCapabilityNames = "CAP_NET_ADMIN, CAP_NET_RAW"

// setcapHint is the Linux remedy that is quoted in three places (the engine
// error, `vender tun status`, `vender autostart status`), so it lives once.
func setcapHint(engineBinary string) string {
	path := engineBinary
	if path == "" {
		path = "<path-to-sing-box>"
	}
	return "sudo setcap cap_net_admin,cap_net_raw+ep " + path
}

// indentHint indents a multi-line hint under a verdict, so the composite
// message reads as one paragraph rather than three.
func indentHint(hint string) string {
	if hint == "" {
		return ""
	}
	lines := strings.Split(hint, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
