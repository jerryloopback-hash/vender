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
	// Type is a sing-box DNS transport: "udp", "tls", "https" or "fakeip".
	Type string
	// Server is the resolver address. An IP literal, so the resolver itself
	// never needs a resolver. Empty for the fakeip type, which has no upstream.
	Server string
	// Detour is the outbound tag the query is sent through; empty means
	// "direct". A DNS server with an empty detour must not be given an empty
	// `direct` outbound: sing-box 1.14 rejects "detour to an empty direct
	// outbound".
	Detour string
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

// RuleModeDNS builds the rule-mode DNS policy, including the user's direct
// list. The design, and why it cannot hang on a dead proxy:
//
//   - Every server is either local or reached directly. No query is ever sent
//     through the proxy, so a slow or dead proxy cannot stall name resolution.
//   - Domains that will be routed directly (the critical list, the user's
//     direct list, geosite-cn) are resolved for real by DNSCN. Their addresses
//     are correct and immediately usable.
//   - Everything else — i.e. everything that will be routed through the proxy
//     — is answered by DNSFakeIP: a synthetic address, handed out locally in
//     microseconds, with the domain preserved so the proxy receives the name
//     (and lets the server resolve it where the domain actually lives, which
//     also means a poisoned domestic answer can never be used for a proxied
//     connection).
//
// The rule order is the policy: critical, user direct, Chinese, then the
// FakeIP catch-all for address queries. `final` is DNSCN rather than FakeIP so
// non-address queries (HTTPS/SVCB, TXT, ...) for unmatched names still get a
// real answer; the domain is exposed to the domestic resolver, which is the one
// deliberate trade-off of this design (documented in docs/ARCHITECTURE.md).
func RuleModeDNS(user *UserRules) DNS {
	rules := []DNSRule{{
		Name:           "critical-direct-real",
		DomainSuffixes: CriticalDirectSuffixes(),
		Server:         DNSCN,
	}}
	if domains, suffixes := user.DirectSuffixes(); len(domains)+len(suffixes) > 0 {
		rules = append(rules, DNSRule{
			Name:           "user-direct-real",
			Domains:        domains,
			DomainSuffixes: suffixes,
			Server:         DNSCN,
		})
	}
	rules = append(rules,
		DNSRule{Name: "cn-domains-real", RuleSets: []string{"geosite-cn"}, Server: DNSCN},
		DNSRule{Name: "proxied-fakeip", QueryTypes: []string{"A", "AAAA"}, Server: DNSFakeIP},
	)
	return DNS{
		Servers: []DNSServer{
			{Tag: DNSCN, Type: "udp", Server: "223.5.5.5"},
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
