package rules

// DNS server tags vender generates. The names are part of the rendered config
// only; nothing outside this package needs to know them except the renderer.
const (
	// DNSCN is the domestic resolver, used for domains that will be routed
	// directly (Chinese domains, the critical list, the user's direct list) and
	// for resolving destinations that a direct outbound has to dial by name. It
	// is reached directly, so it never depends on the proxy being healthy.
	DNSCN = "dns-cn"
	// DNSFakeIP is the local FakeIP resolver. It answers address queries for
	// proxied domains instantly from a reserved range, without asking anyone —
	// which is precisely why name resolution can never hang on a dead proxy.
	// The engine maps the fake address back to the domain before dialling, so
	// the proxy still receives the real name.
	DNSFakeIP = "dns-fakeip"
	// DNSRemote is the foreign resolver reached *through the proxy*. It is used
	// only in global mode, where the whole point is that nothing escapes the
	// tunnel; rule mode never routes a query through the proxy.
	DNSRemote = "dns-remote"
)

// BuiltinDirectDNSAddress is vender's explicit direct DNS default, shared by
// rule-mode DNS and direct Ping when no profile resolver applies.
const BuiltinDirectDNSAddress = "223.5.5.5"

// DNSResolverStrategy is sing-box's address-family preference. prefer_ipv4
// keeps behaviour predictable on dual-stack hosts (a v4 answer first, v6 as a
// fallback) and avoids the "IPv6 route silently broken" class of failure.
const DNSResolverStrategy = "prefer_ipv4"

// FakeIP ranges. 198.18.0.0/15 is the range sing-box documents; vender's TUN
// interface address is chosen outside it (see config.DefaultTunAddress), so the
// two cannot collide.
const (
	FakeIPv4Range = "198.18.0.0/15"
	FakeIPv6Range = "fc00::/18"
)

// DNSServer is one upstream resolver in the generated `dns.servers` list.
type DNSServer struct {
	// Tag is how DNS rules refer to this server.
	Tag string
	// Type is a sing-box DNS transport: "udp", "tls", "https", "fakeip" or
	// "local".
	Type string
	// Server is the resolver address. An IP literal, so the resolver itself
	// never needs a resolver. Empty for the fakeip and local types, which
	// have no upstream address.
	Server string
	// Port is the resolver's port. Zero keeps sing-box's per-type default
	// (53 for udp/tcp, 853 for tls, 443 for https).
	Port int
	// Path is the DoH endpoint path (type "https" only); empty keeps
	// sing-box's /dns-query.
	Path string
	// Detour is the outbound tag the query is sent through; empty means
	// "direct". A DNS server with an empty detour must not be given an empty
	// `direct` outbound: sing-box 1.14 rejects "detour to an empty direct
	// outbound".
	Detour string
	// BindInterface pins the resolver's own dial to an interface, exactly as
	// it does for an outbound. It is set for a profile's own resolvers when
	// engine.bind_interface is, so they are reached directly on a multi-homed
	// or TUN host.
	BindInterface string
	// DomainResolver resolves *this resolver's own* endpoint hostname through
	// another DNS server. It is what lets a name-based resolver (the
	// provider's DNS is one) be reached directly, without a bootstrap that
	// depends on the split policy.
	DomainResolver string
	// Inet4Range/Inet6Range are the FakeIP pools (type "fakeip" only).
	Inet4Range string
	Inet6Range string
}

// DNSRule routes DNS queries for a set of destinations to one server.
//
// It is deliberately wider than "rule set -> server": the split that keeps DNS
// off the proxy needs to match exact domains and suffixes (the critical and
// user lists) and the query type (only address queries may go to FakeIP).
type DNSRule struct {
	// Action defaults to route; reject mirrors a blocked domain without an
	// upstream lookup. Server is empty for reject rules.
	Action string
	// Name is a human-readable identifier.
	Name string
	// RuleSets lists the destination rule sets this rule applies to. A DNS
	// rule set must also be declared in route.rule_set, which is why the
	// renderer emits the same tags for both.
	RuleSets []string
	// Domains matches exact query names.
	Domains []string
	// DomainSuffixes matches query-name suffixes.
	DomainSuffixes []string
	// QueryTypes restricts the rule to specific query types, e.g. "A"/"AAAA".
	QueryTypes []string
	// Server is the DNS server tag that answers matching queries.
	Server string
}

// DNS is vender's DNS policy, rendered into sing-box's top-level `dns` section.
type DNS struct {
	// Servers are the upstream resolvers.
	Servers []DNSServer
	// Rules split queries between them; empty means "use Final for everything".
	Rules []DNSRule
	// Final answers everything no rule matched.
	Final string
	// Strategy is the address-family preference.
	Strategy string
}

// BuiltinDNS returns the split DNS policy used in rule mode, without any user
// additions. It is what `vender rules list` shows and what tests compare
// against; the engine is rendered with RuleModeDNS(user) instead.
func BuiltinDNS() DNS { return RuleModeDNS(nil) }

// RuleModeDNS projects the domain-matchable portion of EffectiveRules into
// DNS rules in the same order. IP-only conditions cannot be evaluated before
// resolution and are intentionally not projected. The design:
//
//   - Every server is either local or reached directly. No query is ever sent
//     through the proxy, so a slow or dead proxy cannot stall name resolution.
//   - Direct domain rules use DNSCN; blocked domain rules reject the query.
//   - Proxy domain rules precede CN rules, including where sets overlap.
//   - Everything else — i.e. everything that will be routed through the proxy
//     — is answered by DNSFakeIP: a synthetic address, handed out locally in
//     microseconds, with the domain preserved so the proxy receives the name
//     (and lets the server resolve it where the domain actually lives, which
//     also means a poisoned domestic answer can never be used for a proxied
//     connection).
//
// `final` is DNSCN rather than FakeIP so
// non-address queries (HTTPS/SVCB, TXT, ...) for unmatched names still get a
// real answer; the domain is exposed to the domestic resolver, which is the one
// deliberate trade-off of this design (documented in docs/ARCHITECTURE.md).
func RuleModeDNS(user *UserRules) DNS {
	// Invalid user entries are rejected by the caller's configuration/route
	// validation. Do not build a partial DNS policy from an invalid list.
	policy, err := EffectiveRules(user)
	if err != nil {
		policy, _ = EffectiveRules(nil)
	}
	var rules []DNSRule
	for _, route := range policy {
		r := DNSRule{Domains: route.Domains, DomainSuffixes: route.DomainSuffixes}
		for _, tag := range route.RuleSets {
			if set, ok := LookupRuleSet(tag); ok && set.Kind() == "geosite" {
				r.RuleSets = append(r.RuleSets, tag)
			}
		}
		if len(r.RuleSets)+len(r.Domains)+len(r.DomainSuffixes) == 0 {
			continue
		}
		switch route.Outbound {
		case OutDirect:
			r.Name, r.Server = route.Name+"-real", DNSCN
			if route.Name == "cn-direct" {
				r.Name = "cn-domains-real"
			}
		case OutProxy:
			r.Name, r.Server = route.Name+"-fakeip", DNSFakeIP
			r.QueryTypes = []string{"A", "AAAA"}
		case OutBlock:
			r.Name, r.Action = route.Name+"-reject", "reject"
		}
		rules = append(rules, r)
	}
	rules = append(rules,
		DNSRule{Name: "proxied-fakeip", QueryTypes: []string{"A", "AAAA"}, Server: DNSFakeIP},
	)
	return DNS{
		Servers: []DNSServer{
			{Tag: DNSCN, Type: "udp", Server: BuiltinDirectDNSAddress},
			{Tag: DNSFakeIP, Type: "fakeip", Inet4Range: FakeIPv4Range, Inet6Range: FakeIPv6Range},
		},
		Rules:    rules,
		Final:    DNSCN,
		Strategy: DNSResolverStrategy,
	}
}

// GlobalDNS returns the DNS policy used in global mode: there are no rules to
// split on, and every connection is proxied anyway, so every query is resolved
// through the proxy too. That keeps global mode from leaking DNS in a mode
// whose entire promise is "nothing escapes".
func GlobalDNS() DNS {
	return DNS{
		Servers: []DNSServer{
			{Tag: DNSRemote, Type: "https", Server: "1.1.1.1", Detour: string(OutProxy)},
		},
		Final:    DNSRemote,
		Strategy: DNSResolverStrategy,
	}
}

// ReferencedRuleSets returns the tags every rule in the built-in policy refers
// to, in the pinned-table order and without duplicates.
func ReferencedRuleSets() []string {
	needed := map[string]bool{}
	for _, r := range BuiltinRules() {
		for _, tag := range r.RuleSets {
			needed[tag] = true
		}
	}
	for _, r := range BuiltinDNS().Rules {
		for _, tag := range r.RuleSets {
			needed[tag] = true
		}
	}
	var tags []string
	for _, rs := range ruleSets {
		if needed[rs.Tag] {
			tags = append(tags, rs.Tag)
		}
	}
	return tags
}
