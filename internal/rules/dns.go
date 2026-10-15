package rules

// DNS server tags vender generates. The names are part of the rendered config
// only; nothing outside this package needs to know them except the renderer.
const (
	// DNSCN is the domestic resolver, used for Chinese domains and for
	// resolving destinations that a direct outbound has to dial by name.
	DNSCN = "dns-cn"
	// DNSRemote is the foreign resolver, reached *through the proxy*, so
	// queries for foreign domains are answered where those domains exist
	// instead of by a local resolver that may be poisoned or censored.
	DNSRemote = "dns-remote"
)

// DNSResolverStrategy is sing-box's address-family preference. prefer_ipv4
// keeps behaviour predictable on dual-stack hosts (a v4 answer first, v6 as a
// fallback) and avoids the "IPv6 route silently broken" class of failure.
const DNSResolverStrategy = "prefer_ipv4"

// DNSServer is one upstream resolver in the generated `dns.servers` list.
type DNSServer struct {
	// Tag is how DNS rules refer to this server.
	Tag string
	// Type is a sing-box DNS transport: "udp", "tls" or "https".
	Type string
	// Server is the resolver address. An IP literal, so the resolver itself
	// never needs a resolver.
	Server string
	// Detour is the outbound tag the query is sent through; empty means
	// "direct". A DNS server with an empty detour must not be given an empty
	// `direct` outbound: sing-box 1.14 rejects "detour to an empty direct
	// outbound".
	Detour string
}

// DNSRule routes DNS queries for a set of destinations to one server.
type DNSRule struct {
	// Name is a human-readable identifier.
	Name string
	// RuleSets lists the destination rule sets this rule applies to. A DNS
	// rule set must also be declared in route.rule_set, which is why the
	// renderer emits the same tags for both.
	RuleSets []string
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

// BuiltinDNS returns the split DNS policy used in rule mode.
//
// The split follows the routing split on purpose: if a name is resolved by one
// resolver but dialled through the other side of the world the result is stale
// or plain wrong, which is how "smart routing" configurations end up with a
// Chinese CDN's IP in a connection that is leaving through a foreign server.
//
//	geosite-cn            -> dns-cn      (223.5.5.5 over UDP, direct)
//	everything else       -> dns-remote  (Cloudflare DoH, through the proxy)
//
// The remote resolver is addressed by IP and reached through the `proxy`
// outbound, so the query never appears on the local network.
func BuiltinDNS() DNS {
	return DNS{
		Servers: []DNSServer{
			{Tag: DNSCN, Type: "udp", Server: "223.5.5.5"},
			{Tag: DNSRemote, Type: "https", Server: "1.1.1.1", Detour: string(OutProxy)},
		},
		Rules: []DNSRule{
			{Name: "cn-domains-via-cn-dns", RuleSets: []string{"geosite-cn"}, Server: DNSCN},
		},
		Final:    DNSRemote,
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
