package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// fakeReg records the registry writes in order and can be told to fail one of
// them, which is what makes the "endpoints before the switch" ordering
// testable without a Windows registry.
type fakeReg struct {
	ops    []string
	failOn string
}

func (f *fakeReg) record(op string) error {
	f.ops = append(f.ops, op)
	if f.failOn != "" && strings.HasPrefix(op, f.failOn) {
		return errors.New("simulated registry failure")
	}
	return nil
}

func (f *fakeReg) SetString(subkey, name, value string) error {
	return f.record("SetString " + name + "=" + value)
}

func (f *fakeReg) SetDWORD(subkey, name string, value uint32) error {
	return f.record("SetDWORD " + name + "=" + strconv.FormatUint(uint64(value), 10))
}

func TestApplyWindowsProxyWritesEndpointsBeforeTheSwitch(t *testing.T) {
	reg := &fakeReg{}
	refreshes := 0
	if err := applyWindowsProxy(reg, func() error { refreshes++; return nil }, true, "127.0.0.1:7897"); err != nil {
		t.Fatalf("applyWindowsProxy: %v", err)
	}

	want := []string{
		"SetString ProxyServer=http=127.0.0.1:7897;https=127.0.0.1:7897;socks=127.0.0.1:7897",
		"SetString ProxyOverride=" + winProxyBypass,
		"SetDWORD ProxyEnable=1",
	}
	if len(reg.ops) != len(want) {
		t.Fatalf("ops = %v, want %v", reg.ops, want)
	}
	for i := range want {
		if reg.ops[i] != want[i] {
			t.Errorf("op %d = %q, want %q", i, reg.ops[i], want[i])
		}
	}
	if refreshes != 1 {
		t.Errorf("WinINET refreshes = %d, want 1", refreshes)
	}
}

func TestApplyWindowsProxyOffClearsOnlyTheSwitch(t *testing.T) {
	reg := &fakeReg{}
	refreshes := 0
	if err := applyWindowsProxy(reg, func() error { refreshes++; return nil }, false, "127.0.0.1:7897"); err != nil {
		t.Fatalf("applyWindowsProxy: %v", err)
	}
	if len(reg.ops) != 1 || reg.ops[0] != "SetDWORD ProxyEnable=0" {
		t.Errorf("ops = %v, want exactly the ProxyEnable switch", reg.ops)
	}
	if refreshes != 1 {
		t.Errorf("turning the proxy off did not refresh WinINET")
	}
}

func TestApplyWindowsProxyPartialFailureLeavesItOff(t *testing.T) {
	// The server value is written first; if that fails the switch must never
	// flip, so the machine is left with the proxy disabled rather than pointed
	// at a listener that may not exist.
	reg := &fakeReg{failOn: "SetString ProxyServer"}
	if err := applyWindowsProxy(reg, func() error { return nil }, true, "127.0.0.1:7897"); err == nil {
		t.Fatal("a failing write did not surface an error")
	}
	for _, op := range reg.ops {
		if strings.HasPrefix(op, "SetDWORD ProxyEnable=1") {
			t.Errorf("the proxy switch flipped despite a failed write: %v", reg.ops)
		}
	}
}

func TestApplyWindowsProxyRejectsABadListenAddress(t *testing.T) {
	reg := &fakeReg{}
	if err := applyWindowsProxy(reg, nil, true, "not-an-address"); err == nil {
		t.Error("a bad listen address was accepted")
	}
	if len(reg.ops) != 0 {
		t.Errorf("nothing should be written for a bad address, got %v", reg.ops)
	}
}

func TestWinProxyServerValueBracketsIPv6(t *testing.T) {
	got := winProxyServerValue("::1", "7897")
	if !strings.Contains(got, "http=[::1]:7897") {
		t.Errorf("winProxyServerValue(::1) = %q", got)
	}
}
