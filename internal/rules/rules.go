// Package rules supplies vender's built-in routing rules — the default
// "smart routing" policy — and the compiled rule sets they match against.
//
// The policy is deliberately tiny. The reference configuration this is derived
// from carries ~9500 individual rules (9018 of them DOMAIN-SUFFIX); vender
// aggregates the same intent into a handful of geosite/geoip categories that
// ship inside the binary, so the default behaves like the hand-written list
// without carrying (or rotting with) thousands of domains.
//
// Order is the whole policy: the first matching rule wins, and it is the only
// reason the service overrides sit where they do.
package rules

import "fmt"

// Outbound is the target a matching connection is sent to.
type Outbound string

const (
	// OutDirect bypasses the proxy (direct connection).
	OutDirect Outbound = "direct"
	// OutProxy sends the connection through the selected server.
	OutProxy Outbound = "proxy"
	// OutBlock rejects the connection.
	OutBlock Outbound = "block"
)

// Rule is one routing rule, expressed in vender's own terms. core converts it
// to sing-box's route rule JSON (rule_set, domain_suffix, ip_cidr, port, ...).
type Rule struct {
	// Name is a human-readable identifier, e.g. "cn-direct".
	Name string
	// Outbound is where matches are sent.
	Outbound Outbound
	// RuleSets matches destinations covered by the named built-in rule sets
	// (see RuleSets()). Tags are validated by Validate.
	RuleSets []string
	// Domains matches exact domain names.
	Domains []string
	// DomainSuffixes matches suffixes, e.g. "cn" matches "example.cn".
	DomainSuffixes []string
	// DomainKeywords matches substrings anywhere in the domain.
	DomainKeywords []string
	// IPCIDRs matches destination IP ranges.
	IPCIDRs []string
	// IPIsPrivate matches destinations whose IP is private (RFC1918),
	// loopback or link-local. It is a single flag rather than a CIDR list
	// because sing-box has a dedicated `ip_is_private` matcher, and spelling
	// the ranges out by hand is how such lists go stale.
	IPIsPrivate bool
	// Ports matches destination ports.
	Ports []int
	// ProcessNames matches the local process name (Linux) / executable (Windows).
	ProcessNames []string
	// DNSOnly marks a rule used only during DNS resolution.
	DNSOnly bool
}

// Validate checks that a rule says something vender can render and that every
// rule set it references is actually embedded.
func (r Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rules: rule name must not be empty")
	}
	switch r.Outbound {
	case OutDirect, OutProxy, OutBlock:
	default:
		return fmt.Errorf("rules: rule %q has unknown outbound %q", r.Name, r.Outbound)
	}
	for _, tag := range r.RuleSets {
		if _, ok := LookupRuleSet(tag); !ok {
			return fmt.Errorf("rules: rule %q references unknown rule set %q", r.Name, tag)
		}
	}
	if len(r.RuleSets)+len(r.Domains)+len(r.DomainSuffixes)+len(r.DomainKeywords)+
		len(r.IPCIDRs)+len(r.Ports)+len(r.ProcessNames) == 0 && !r.IPIsPrivate {
		return fmt.Errorf("rules: rule %q matches nothing", r.Name)
	}
	return nil
}

// BuiltinRules returns vender's default rule set, in evaluation order.
//
// A nil/empty result means "no routing rules configured" (route everything
// through the proxy), not "match nothing".
func BuiltinRules() []Rule {
	return []Rule{
		// 1. Never sweep private traffic through a remote server: RFC1918,
		// loopback and link-local destinations are the local network, and a
		// NAS, a printer or a router admin page must keep working.
		//
		// Only the destination is inspected. Matching the *source* would be
		// meaningless here and actively harmful: the mixed SOCKS5/HTTP inbound
		// only ever accepts connections from localhost, so `source_ip_is_private`
		// would match every single connection and send everything direct.
		{
			Name:        "private-dest-direct",
			Outbound:    OutDirect,
			IPIsPrivate: true,
		},

		// 2. Ads, trackers and telemetry are rejected outright rather than
		// proxied, which is what the 1596 "拦截广告" rules were for.
		{
			Name:     "ads-block",
			Outbound: OutBlock,
			RuleSets: []string{"geosite-category-ads-all"},
		},

		// Ordinary Bing search and its image/static hosts are reachable from
		// mainland China. Match search entry points exactly: a bing.com suffix
		// would also exempt edgeservices.bing.com / sydney.bing.com (Copilot).
		// Paths on www.bing.com cannot be split by domain-based routing.
		{
			Name:     "bing-search-direct",
			Outbound: OutDirect,
			Domains: []string{
				"bing.com", "www.bing.com", "cn.bing.com", "global.bing.com",
				"bing.com.cn", "www.bing.com.cn", "s.bing.net",
			},
			DomainSuffixes: []string{"r.bing.com", "th.bing.com", "cn.bing.net", "mm.bing.net"},
		},

		// 3. Service overrides. These must come *before* the China rule,
		// because several of them (Steam, Microsoft, Google) have China-facing
		// entries in geosite-cn that would otherwise claim them. These broad
		// service policies intentionally proxy even overlapping CN entries,
		// except for explicit direct exceptions above (ordinary Bing search).
		//
		// Keep this list short and justified; adding a category here costs a
		// proxy hop for every connection it contains.
		{
			Name:     "service-overrides-proxy",
			Outbound: OutProxy,
			RuleSets: []string{
				"geosite-steam",     // store / community / download CDNs
				"geosite-onedrive",  // sync endpoints are throttled from CN
				"geosite-microsoft", // includes CN services and GitHub; Bing search is exempt above
				"geosite-google",
				"geosite-youtube",
				"geosite-telegram",
				"geosite-netflix",
				"geosite-openai",
				"geosite-bahamut", // 動畫瘋 / gamer.com.tw, region-locked
			},
		},

		// 4. Everything recognizably Chinese goes out directly: the geosite
		// list by domain, geoip-cn by address so bare-IP traffic is covered
		// even when nothing can be sniffed (a plain TCP stream, say).
		{
			Name:     "cn-direct",
			Outbound: OutDirect,
			RuleSets: []string{"geosite-cn", "geoip-cn"},
		},

		// 5. route.final (see core.render) sends the rest through the proxy.
	}
}

// EffectiveRules is the routing policy actually rendered in rule mode: the
// built-in critical list first, then the user's own file, then the built-in
// policy. A user entry therefore always wins over the built-in lists, and the
// critical list wins over everything — which is what keeps the machine
// manageable when a user rule or a built-in category would otherwise send a
// control-plane domain through a dead proxy.
func EffectiveRules(user *UserRules) ([]Rule, error) {
	userRules, err := user.Rules()
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(userRules)+len(BuiltinRules())+1)
	out = append(out, CriticalRules()...)
	out = append(out, userRules...)
	out = append(out, BuiltinRules()...)
	return out, nil
}

// ActionKind names a sing-box route action. Actions change what the engine
// does with a connection instead of choosing an outbound for it, and they are
// emitted before any destination rule.
type ActionKind string

const (
	// ActionSniff asks the engine to inspect the first bytes of a connection
	// to recover the real protocol and domain name. Without it, a connection
	// addressed to an IP is just an IP and no domain rule can match it.
	ActionSniff ActionKind = "sniff"
	// ActionHijackDNS hands a DNS connection to the engine's DNS module
	// instead of routing it as plain traffic, so the split DNS policy below
	// applies even to applications that send their queries "through" the
	// proxy as UDP.
	ActionHijackDNS ActionKind = "hijack-dns"
)

// Action is a route action rule.
type Action struct {
	// Kind is the action to take.
	Kind ActionKind
	// Protocol optionally restricts the action to a sniffed protocol, e.g.
	// "dns" for hijack-dns.
	Protocol string
}

// BuiltinActions returns the actions that must precede the destination rules.
func BuiltinActions() []Action {
	return []Action{
		{Kind: ActionSniff},
		{Kind: ActionHijackDNS, Protocol: "dns"},
	}
}
