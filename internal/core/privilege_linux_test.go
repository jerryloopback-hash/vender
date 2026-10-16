//go:build linux

package core

import (
	"os"
	"strings"
	"testing"
)

// Capability masks are hex, 64 bits wide, and parsed rather than shifted as an
// int64 (which would overflow on bit 63).
func TestCapabilitySet(t *testing.T) {
	for _, tt := range []struct {
		mask string
		bit  uint
		want bool
	}{
		{"000001ffffffffff", 12, true},  // CAP_NET_ADMIN, inside the bounding set
		{"0000000000001000", 12, true},  // exactly CAP_NET_ADMIN
		{"0000000000000000", 12, false}, // nothing at all
		{"0000000000002000", 12, false}, // CAP_NET_RAW only
		{"0000000000002000", 13, true},  // CAP_NET_RAW
		{"8000000000000000", 63, true},  // the top bit, which an int64 shift would break
		{"8000000000000000", 62, false},
		{"0x0000000000001000", 12, true}, // tolerated prefix
		{"", 12, false},
		{"zzzz", 12, false},
		{"0000000000001000", 64, false}, // out of range
	} {
		if got := capabilitySet(tt.mask, tt.bit); got != tt.want {
			t.Errorf("capabilitySet(%q, %d) = %v, want %v", tt.mask, tt.bit, got, tt.want)
		}
	}
}

func TestParseStatusField(t *testing.T) {
	status := "Name:\tvender\nUid:\t1000\t1000\t1000\t1000\nCapEff:\t0000000000000000\nCapBnd:\t000001ffffffffff\n"
	if v, ok := parseStatusField(status, "CapEff"); !ok || v != "0000000000000000" {
		t.Errorf("CapEff = %q, %v", v, ok)
	}
	if v, ok := parseStatusField(status, "CapBnd"); !ok || v != "000001ffffffffff" {
		t.Errorf("CapBnd = %q, %v", v, ok)
	}
	if _, ok := parseStatusField(status, "CapPrm"); ok {
		t.Error("found a field that is not there")
	}
	// "Cap" must not match "CapBnd" by prefix.
	if _, ok := parseStatusField(status, "Cap"); ok {
		t.Error("prefix matching is too loose")
	}
}

// The file-capability decode is checked against a real xattr value: a copy of
// /usr/bin/ping's `security.capability` (VFS_CAP_REVISION_2, CAP_NET_RAW
// permitted *and* effective) and the same value with the effective flag
// cleared. The second case is the one that matters — permitted-but-not-
// effective capabilities still fail capable(CAP_NET_ADMIN).
func TestFileCapabilityGrants(t *testing.T) {
	ping := []byte{0x01, 0x00, 0x00, 0x02, 0x00, 0x20, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if !fileCapabilityGrants(ping, 13) { // CAP_NET_RAW
		t.Error("ping's capability is not recognised as effective CAP_NET_RAW")
	}
	if fileCapabilityGrants(ping, 12) { // CAP_NET_ADMIN
		t.Error("ping is reported to hold CAP_NET_ADMIN")
	}

	notEffective := append([]byte(nil), ping...)
	notEffective[0] = 0x00 // clear VFS_CAP_FLAGS_EFFECTIVE
	if fileCapabilityGrants(notEffective, 13) {
		t.Error("a permitted-but-not-effective capability was accepted")
	}

	netAdmin := []byte{0x01, 0x00, 0x00, 0x02, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if !fileCapabilityGrants(netAdmin, 12) {
		t.Error("cap_net_admin+ep is not recognised")
	}

	// Revision 1 is the obsolete 32-bit form, and a truncated value is not a
	// capability at all.
	if fileCapabilityGrants([]byte{0x01, 0x00, 0x00, 0x01, 0x00, 0x10, 0x00, 0x00}, 12) {
		t.Error("a revision-1 header was accepted")
	}
	if fileCapabilityGrants([]byte{0x01, 0x00}, 12) {
		t.Error("a truncated xattr was accepted")
	}
	if fileCapabilityGrants(nil, 12) {
		t.Error("an empty xattr was accepted")
	}
}

// A binary without the xattr yields no verdict, not a false one.
func TestFileCapabilityVerdictWithoutXattr(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "no-caps-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	name := f.Name()
	f.Close()
	if got := fileCapabilityVerdict(name); got != "" {
		t.Errorf("fileCapabilityVerdict(%s) = %q, want empty", name, got)
	}
	if got := fileCapabilityVerdict(""); got != "" {
		t.Errorf("fileCapabilityVerdict(\"\") = %q, want empty", got)
	}
}

// The verdict is always usable: an OK verdict explains itself, and a failure
// carries an actionable hint naming the capability.
func TestCheckTUNPrivilegeIsAlwaysActionable(t *testing.T) {
	p := CheckTUNPrivilege("/nonexistent/sing-box")
	if p.OK {
		if p.Detail == "" {
			t.Error("an OK verdict has no detail")
		}
		if p.Err() != nil {
			t.Errorf("an OK verdict produced an error: %v", p.Err())
		}
		return
	}
	if p.Hint == "" {
		t.Error("a failing verdict has no hint")
	}
	err := p.Err()
	if err == nil {
		t.Fatal("a failing verdict produced no error")
	}
	if !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
		t.Errorf("error = %v, want it to name CAP_NET_ADMIN", err)
	}
	// The hint must name the actual binary it will be granted to.
	failed := CheckTUNPrivilege("/opt/vender/sing-box")
	if !failed.OK && !strings.Contains(failed.Hint, "/opt/vender/sing-box") {
		t.Errorf("hint does not name the engine binary:\n%s", failed.Hint)
	}
}

// On this machine — an unprivileged account, no setcap — the check must say no,
// and vender must not pretend otherwise. The assertion is skipped where the
// test runs as root or with the capability, which is a legitimate CI setup.
func TestCheckTUNPrivilegeOnThisHost(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: TUN mode is permitted, nothing to assert")
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("no /proc/self/status: %v", err)
	}
	capEff, ok := parseStatusField(string(raw), "CapEff")
	if !ok || capabilitySet(capEff, capabilityNetAdmin) {
		t.Skipf("CAP_NET_ADMIN is already held (CapEff %s)", capEff)
	}
	p := CheckTUNPrivilege("/nonexistent/sing-box")
	if p.OK {
		t.Errorf("the check passed without root, without CAP_NET_ADMIN and without a setcap binary: %+v", p)
	}
}
