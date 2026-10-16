package config

import (
	"fmt"
	"net"
	"runtime"
	"strings"
)

// TUN stacks accepted in engine.tun.stack. They are sing-box's own names:
//
//	system  the kernel's TCP/IP stack, reached through the TUN device
//	gvisor  a user-space stack, no kernel routing of the payload
//	mixed   TCP on gvisor, UDP on the system stack (sing-box's default)
//
// They are validated here because the engine accepts an unknown stack string
// without complaining (`sing-box check` passes for stack: "bogus"), so a typo
// would otherwise turn into a silent behaviour change.
const (
	TunStackSystem = "system"
	TunStackGvisor = "gvisor"
	TunStackMixed  = "mixed"
)

const (
	// DefaultTunStack is the stack vender chooses when the user does not.
	DefaultTunStack = TunStackMixed
	// DefaultTunInterfaceName is the device name vender asks for. It is
	// explicit rather than left to the engine so `ip link`, `ip route` and the
	// cleanup after a crash all have one predictable name to look for.
	//
	// `address` is deliberately left empty by default instead: sing-box then
	// picks its own private /30, which is known not to collide with the
	// 198.18.0.0/15 range mihomo/clash uses for its own TUN.
	DefaultTunInterfaceName = "vender0"
)

// ifNameMaxLen is Linux's IFNAMSIZ-1. Windows accepts longer adapter names, so
// the limit is applied only where the kernel enforces it.
const ifNameMaxLen = 15

// TunConfig configures the optional TUN inbound: transparent, system-wide
// traffic capture. It is off by default — vender's default posture is a
// per-application mixed inbound, and TUN mode changes the host's routing table.
//
// Every field maps onto one sing-box `tun` inbound field; the names are kept
// identical so a user who knows sing-box can read vender's config.yaml without
// a translation table.
type TunConfig struct {
	// Enabled turns the TUN inbound on. It needs CAP_NET_ADMIN (Linux) or an
	// elevated process (Windows); core.CheckTUNPrivilege reports the verdict
	// before the engine is started.
	Enabled bool `yaml:"enabled"`
	// Stack is system, gvisor or mixed.
	Stack string `yaml:"stack"`
	// MTU is the device MTU. 0 means "let the engine choose" (sing-box uses
	// 9000); anything else must be at least 1280 for IPv6 to work.
	MTU int `yaml:"mtu"`
	// AutoRoute installs the routes that pull every destination into the TUN.
	// It is a pointer so that an absent key means the default (true) while
	// `auto_route: false` is honoured as written: with auto_route off nothing
	// is captured until a route is added by hand, which is the safe way to try
	// a TUN on a machine whose connectivity matters.
	AutoRoute *bool `yaml:"auto_route"`
	// StrictRoute refuses traffic that would bypass the tunnel instead of
	// letting it out through the physical interface. It is a kill switch: with
	// it on, a broken engine means no connectivity at all, so it stays off by
	// default.
	StrictRoute bool `yaml:"strict_route"`
	// InterfaceName is the device name (vender0 by default).
	InterfaceName string `yaml:"interface_name"`
	// Address is the TUN's own address, as CIDR strings. Empty means the
	// engine's default.
	Address []string `yaml:"address,omitempty"`
	// RouteExcludeAddress lists CIDRs that are *not* routed into the TUN —
	// the usual use is sparing a LAN, another VPN (mihomo's 198.18.0.0/15), or
	// a link-local range. Passed through to sing-box verbatim.
	RouteExcludeAddress []string `yaml:"route_exclude_address,omitempty"`
	// RouteExcludeAddressSet is RouteExcludeAddress expressed as embedded rule
	// set tags (geoip-cn, geoip-private, ...). Passed through to sing-box's
	// route_exclude_address_set; vender checks the tags exist and declares them
	// in route.rule_set so the generated config validates.
	//
	// sing-box 1.14 has no `route_exclude_rule` field; a rule-set based
	// exclusion is the closest equivalent, and this is it.
	RouteExcludeAddressSet []string `yaml:"route_exclude_address_set,omitempty"`
}

// Bool returns a pointer to v. It exists so callers can build a TunConfig with
// an explicit AutoRoute.
func Bool(v bool) *bool { return &v }

// DefaultTun returns vender's TUN defaults: off, mixed stack, engine-chosen
// MTU, auto_route on, no exclusions, device vender0.
func DefaultTun() TunConfig {
	return TunConfig{
		Enabled:       false,
		Stack:         DefaultTunStack,
		MTU:           0,
		AutoRoute:     Bool(true),
		StrictRoute:   false,
		InterfaceName: DefaultTunInterfaceName,
	}
}

// AutoRouteEnabled reports whether routes should be installed. A nil pointer
// (a hand-built or partial config) means the documented default, true.
func (t TunConfig) AutoRouteEnabled() bool {
	return t.AutoRoute == nil || *t.AutoRoute
}

// Normalize fills zero values with the documented defaults and canonicalises
// casing, so a partial engine.tun block behaves like a documented one.
func (t *TunConfig) Normalize() {
	if t.Stack == "" {
		t.Stack = DefaultTunStack
	}
	if t.InterfaceName == "" {
		t.InterfaceName = DefaultTunInterfaceName
	}
	if t.AutoRoute == nil {
		t.AutoRoute = Bool(true)
	}
	t.Stack = strings.ToLower(strings.TrimSpace(t.Stack))
	t.InterfaceName = strings.TrimSpace(t.InterfaceName)
	t.Address = normalizeList(t.Address)
	t.RouteExcludeAddress = normalizeList(t.RouteExcludeAddress)
	t.RouteExcludeAddressSet = normalizeList(t.RouteExcludeAddressSet)
}

// Clone returns a deep copy, so two Configs never share a *bool or a slice.
func (t TunConfig) Clone() TunConfig {
	cp := t
	if t.AutoRoute != nil {
		cp.AutoRoute = Bool(*t.AutoRoute)
	}
	cp.Address = append([]string(nil), t.Address...)
	cp.RouteExcludeAddress = append([]string(nil), t.RouteExcludeAddress...)
	cp.RouteExcludeAddressSet = append([]string(nil), t.RouteExcludeAddressSet...)
	return cp
}

// Validate checks the TUN block. A disabled block is inert and is not checked:
// vender must not refuse to start because of a typo in a feature that is off.
//
// Rule-set existence is not checked here — internal/config does not depend on
// internal/rules. core.RuntimeConfig.Validate does that, where the engine's
// knowledge of the embedded sets is available.
func (t TunConfig) Validate() []string {
	if !t.Enabled {
		return nil
	}
	var problems []string

	switch t.Stack {
	case TunStackSystem, TunStackGvisor, TunStackMixed:
	default:
		problems = append(problems, fmt.Sprintf(
			"engine.tun.stack %q is not one of %s, %s, %s", t.Stack, TunStackSystem, TunStackGvisor, TunStackMixed))
	}

	if t.MTU != 0 && (t.MTU < 1280 || t.MTU > 65535) {
		problems = append(problems, fmt.Sprintf(
			"engine.tun.mtu %d is out of range: use 0 (engine default) or 1280-65535", t.MTU))
	}

	if t.InterfaceName == "" {
		problems = append(problems, "engine.tun.interface_name must not be empty")
	} else {
		if strings.ContainsAny(t.InterfaceName, " \t/") {
			problems = append(problems, fmt.Sprintf(
				"engine.tun.interface_name %q must be a device name without spaces or slashes", t.InterfaceName))
		}
		if runtime.GOOS == "linux" && len(t.InterfaceName) > ifNameMaxLen {
			problems = append(problems, fmt.Sprintf(
				"engine.tun.interface_name %q is longer than Linux allows (%d characters)", t.InterfaceName, ifNameMaxLen))
		}
	}

	for _, cidr := range t.Address {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			problems = append(problems, fmt.Sprintf("engine.tun.address %q is not a CIDR: %v", cidr, err))
		}
	}
	for _, cidr := range t.RouteExcludeAddress {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			problems = append(problems, fmt.Sprintf("engine.tun.route_exclude_address %q is not a CIDR: %v", cidr, err))
		}
	}
	for _, tag := range t.RouteExcludeAddressSet {
		if tag == "" {
			problems = append(problems, "engine.tun.route_exclude_address_set must not contain an empty tag")
		}
	}
	return problems
}

// Describe renders the block for `vender tun status`, one "key: value" per
// line.
func (t TunConfig) Describe() []string {
	out := []string{
		fmt.Sprintf("enabled            %s", boolWord(t.Enabled)),
		fmt.Sprintf("stack              %s", t.Stack),
		fmt.Sprintf("interface          %s", t.InterfaceName),
		fmt.Sprintf("auto_route         %s", boolWord(t.AutoRouteEnabled())),
		fmt.Sprintf("strict_route       %s", boolWord(t.StrictRoute)),
	}
	if t.MTU == 0 {
		out = append(out, "mtu                engine default (9000)")
	} else {
		out = append(out, fmt.Sprintf("mtu                %d", t.MTU))
	}
	if len(t.Address) > 0 {
		out = append(out, "address            "+strings.Join(t.Address, ", "))
	}
	if len(t.RouteExcludeAddress) > 0 {
		out = append(out, "route_exclude      "+strings.Join(t.RouteExcludeAddress, ", "))
	}
	if len(t.RouteExcludeAddressSet) > 0 {
		out = append(out, "exclude sets       "+strings.Join(t.RouteExcludeAddressSet, ", "))
	}
	return out
}

// normalizeList trims each entry and drops empties, so a hand-edited YAML list
// with a trailing blank item does not become a validation error.
func normalizeList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
