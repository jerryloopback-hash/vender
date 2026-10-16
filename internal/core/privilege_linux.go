//go:build linux

package core

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// capabilityNetAdmin is CAP_NET_ADMIN: the bit that lets a process create and
// configure a TUN device, add routes and (for auto_redirect) program nftables.
const capabilityNetAdmin = 12

// vfsCapability* are the constants of the `security.capability` xattr's
// little-endian vfs_cap_data header.
const (
	// vfsCapRevision2 is the 64-bit capability revision (two 32-bit words).
	vfsCapRevision2 = 0x02000000
	// vfsCapRevision3 is the revision used when a root id is recorded.
	vfsCapRevision3 = 0x03000000
	// vfsCapFlagsEffective means the file's permitted set is also effective at
	// exec, which is what `setcap ...+ep` asks for.
	vfsCapFlagsEffective = 0x000001
	vfsCapRevisionMask   = 0xff000000
)

// checkTUNPrivilege answers the Linux question: will `sing-box run` be able to
// create a TUN device?
//
// Three ways to say yes, in the order they are cheap to test:
//
//  1. the process is root;
//  2. CAP_NET_ADMIN is already in this process's effective set (it was started
//     by systemd with AmbientCapabilities=, by sudo, or by an ancestor that
//     passed it on);
//  3. the sing-box binary itself carries the capability (`setcap ...+ep`),
//     which the kernel grants at exec time.
func checkTUNPrivilege(engineBinary string) TUNPrivilege {
	if os.Geteuid() == 0 {
		return TUNPrivilege{OK: true, Detail: "running as root (uid 0), which has " + tunCapabilityNames}
	}
	if raw, err := os.ReadFile("/proc/self/status"); err == nil {
		if capEff, ok := parseStatusField(string(raw), "CapEff"); ok {
			if capabilitySet(capEff, capabilityNetAdmin) {
				return TUNPrivilege{OK: true, Detail: "this process holds CAP_NET_ADMIN"}
			}
			if detail := fileCapabilityVerdict(engineBinary); detail != "" {
				return TUNPrivilege{OK: true, Detail: detail}
			}
			return TUNPrivilege{
				OK:     false,
				Detail: fmt.Sprintf("uid %d does not hold CAP_NET_ADMIN (CapEff %s)", os.Geteuid(), capEff),
				Hint:   tunFixHint(engineBinary),
			}
		}
	}
	// Without /proc we cannot tell; do not block a TUN that might work.
	return TUNPrivilege{OK: true, Detail: "could not read /proc/self/status; assuming the engine can create a TUN"}
}

// fileCapabilityVerdict inspects the engine binary's file capabilities and
// returns a one-line verdict, or "" when the binary does not carry
// CAP_NET_ADMIN effectively.
func fileCapabilityVerdict(engineBinary string) string {
	if engineBinary == "" {
		return ""
	}
	// A vfs_cap_data is 20 bytes (24 with the revision-3 root id); 64 is
	// plenty and avoids a two-call size probe.
	buf := make([]byte, 64)
	n, err := unix.Getxattr(engineBinary, "security.capability", buf)
	if err != nil || n <= 0 {
		return ""
	}
	if !fileCapabilityGrants(buf[:n], capabilityNetAdmin) {
		return ""
	}
	return engineBinary + " carries CAP_NET_ADMIN (setcap), so the engine will have it at exec"
}

// tunFixHint is the Linux remedy list. It names the three legitimate ways to
// give the engine the capability, because which one is right depends on how the
// user runs vender.
func tunFixHint(engineBinary string) string {
	return "TUN mode needs " + tunCapabilityNames + ". Pick one:\n" +
		"* grant it to the engine binary:   " + setcapHint(engineBinary) + "\n" +
		"    (survives until the binary is replaced — e.g. by a sing-box upgrade)\n" +
		"* run vender with the capability:   sudo vender daemon\n" +
		"* install a system service that has it:\n" +
		"      sudo vender autostart on\n" +
		"    (with TUN enabled, `vender autostart on` installs a *system* unit with\n" +
		"     AmbientCapabilities, because a systemd *user* unit cannot create a TUN)"
}

// parseStatusField extracts a `Field:\tvalue` line from /proc/self/status.
// Capability fields are hex without a 0x prefix.
func parseStatusField(status, field string) (string, bool) {
	for _, line := range strings.Split(status, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != field {
			continue
		}
		return strings.TrimSpace(value), true
	}
	return "", false
}

// capabilitySet reports whether bit is set in a hex capability mask such as
// "000001ffffffffff". It is parsed by hand rather than as an int64 because the
// mask is 64 bits wide and would overflow a signed shift.
func capabilitySet(mask string, bit uint) bool {
	mask = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(mask), "0x"))
	if mask == "" {
		return false
	}
	// Capability 63 is the highest; the mask is at most 16 hex digits.
	if bit > 63 {
		return false
	}
	v, err := strconv.ParseUint(mask, 16, 64)
	if err != nil {
		return false
	}
	return v&(1<<bit) != 0
}

// fileCapabilityGrants reports whether a `security.capability` xattr grants bit
// in the *effective* set, which is what `setcap ...+ep` produces.
//
// The value is a little-endian vfs_cap_data:
//
//	u32 magic_etc;        revision in the top byte, flags in the low byte
//	u32 permitted[2];     bits 0-31, then 32-63
//	u32 inheritable[2];
//	u32 rootid;           revision 3 only
//
// Linux computes the effective set from `permitted` only when the "effective"
// flag is set; without it a process gets permitted-but-not-effective
// capabilities and `capable(CAP_NET_ADMIN)` still fails.
func fileCapabilityGrants(data []byte, bit uint) bool {
	if len(data) < 12 {
		return false
	}
	le := func(off int) uint32 {
		return uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16 | uint32(data[off+3])<<24
	}
	magic := le(0)
	switch magic & vfsCapRevisionMask {
	case vfsCapRevision2, vfsCapRevision3:
	default:
		return false // revision 1 is 32-bit-only and long obsolete
	}
	if magic&vfsCapFlagsEffective == 0 {
		return false
	}
	if bit < 32 {
		return le(4)&(1<<bit) != 0
	}
	if len(data) < 16 {
		return false
	}
	return le(12)&(1<<(bit-32)) != 0
}
