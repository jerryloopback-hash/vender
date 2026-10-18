package config

import "testing"

func TestWildcardProxyListenersAreRejected(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:7897", "[::]:7897", "[::ffff:0.0.0.0]:7897"} {
		cfg := Default()
		cfg.Proxy.Listen = listen
		if err := cfg.Validate(); err == nil {
			t.Errorf("wildcard listener %s was accepted", listen)
		}
	}
}

func TestDefaultEnabledTUNCapturesBothFamilies(t *testing.T) {
	tun := DefaultTun()
	tun.Enabled = true
	tun.Normalize()
	if len(tun.Address) != 2 || tun.Address[0] != DefaultTunAddress || !tun.HasIPv6Address() {
		t.Fatalf("default TUN must capture both families: %v", tun.Address)
	}
}

func TestExplicitIPv4OnlyTUNIsPreservedAndWarns(t *testing.T) {
	tun := DefaultTun()
	tun.Enabled = true
	tun.Address = []string{"10.99.0.1/30"}
	tun.Normalize()
	if len(tun.Address) != 1 || tun.HasIPv6Address() {
		t.Fatalf("explicit address configuration was overwritten: %v", tun.Address)
	}
	for _, line := range tun.Describe() {
		if line == "warning            IPv6 is not captured by this TUN address configuration" {
			return
		}
	}
	t.Fatal("IPv4-only configuration did not warn about IPv6 bypass")
}
