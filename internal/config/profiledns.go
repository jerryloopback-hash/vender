package config

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Per-profile DNS policy.
//
// A commercial provider ("机场") resolves its own node hostnames with its own
// DNS. The hostnames are opaque "entry" domains (e.g.
// 1.k.o.5.j.m.k.d.1.hk01-ae5.entry.v51124-6.qpon) whose public answers are a
// decoy — a blackhole from outside China, and a shared "China" address that
// does not carry the provider's traffic. The provider's Clash config says
// which resolver to use for them:
//
//	dns:
//	  nameserver-policy:
//	    +.v51124-6.qpon: tcp://f892…fyi:8080
//
// vender captures that policy at import time and stores it next to the
// profile's URL (profiles.yaml). It is *only* about resolving the profile's
// node hostnames — the engine dialling an outbound — and is entirely separate
// from the client-facing split DNS of internal/rules, which resolves what the
// user's applications ask for (and answers everything proxy-bound from FakeIP,
// never touching the network). See docs/ARCHITECTURE.md §7.7.
//
// The shape is deliberately small and documented:
//
//	# profiles.yaml
//	profiles:
//	  - name: provider
//	    url: https://…/link/…
//	    fetched_at: 2026-10-17T15:04:05Z
//	    dns:
//	      namespace_policy:            # Clash dns.nameserver-policy, ordered
//	        - domain: v51124-6.qpon    # normalized: this name and its subdomains
//	          server: tcp://f892…fyi:8080
//	      nameservers:                 # Clash dns.nameserver (captured, see below)
//	        - 119.29.29.29
//	      fallback: []                 # Clash dns.fallback
//	      raw: |                       # the source `dns:` block, for auditing
//	        nameserver-policy:
//	          +.v51124-6.qpon: tcp://…:8080
//
// `nameservers` and `fallback` are captured because they are part of what the
// provider said, but vender does not silently point every node hostname at
// them: the subscription in the field carries nodes under *public* domains
// (entry.rtysjur.quest, entry.fr0528.art) that resolve and work through the
// ordinary split DNS, and rerouting them to the provider's general resolver
// would break them for no reason. Only the explicit, per-domain
// `nameserver-policy` entries change resolution.

// DNSNamespacePolicy is one entry of a subscription's dns.nameserver-policy:
// a domain — normalized to "this name and every subdomain" — and the resolver
// that answers for it. The server keeps the subscription's spelling
// (tcp://host:port, udp://host:port, tls://host:port, https://host/path, or a
// bare host[:port], which means UDP/53).
type DNSNamespacePolicy struct {
	Domain string `yaml:"domain"`
	Server string `yaml:"server"`
}

// ProfileDNS is the DNS policy captured from one profile's subscription. A
// profile without one is nil, and a nil policy renders nothing at all: the
// generated engine configuration stays byte-identical to M7's.
type ProfileDNS struct {
	// NamespacePolicy is the ordered nameserver-policy: first matching entry
	// wins, longest suffix first (see Match).
	NamespacePolicy []DNSNamespacePolicy `yaml:"namespace_policy,omitempty"`
	// Nameservers is the subscription's default resolver list
	// (dns.nameserver). Captured for auditing; not applied to node hostnames
	// (see the package comment above).
	Nameservers []string `yaml:"nameservers,omitempty"`
	// Fallback is the subscription's dns.fallback list (Clash's
	// "answer only if the default failed"). Captured for auditing.
	Fallback []string `yaml:"fallback,omitempty"`
	// Raw is the verbatim `dns:` block the policy was captured from, so what
	// the provider actually said stays auditable even if vender's parsing of
	// it changes.
	Raw string `yaml:"raw,omitempty"`
}

// ResolverAddress is a parsed DNS resolver endpoint from a nameserver-policy.
type ResolverAddress struct {
	// Type is a sing-box DNS server type: "tcp", "udp", "tls" or "https".
	Type string
	// Host is the resolver's address: an IP literal or a domain name.
	Host string
	// Port is the resolver's port.
	Port int
	// Path is the DoH endpoint path (type "https" only).
	Path string
}

// ParseResolverAddress parses a resolver from a subscription's
// nameserver-policy value. Recognized forms:
//
//	tcp://host:port udp://host:port tls://host:port
//	https://host[:port][/path]
//	host[:port]                       (bare => UDP, port 53)
//
// A missing port takes the transport default (53 for tcp/udp, 853 for tls,
// 443 for https). Anything else — a `#` fragment, a `dhcp://`, an empty value —
// is rejected by name so a bad policy fails loudly at import rather than
// silently at dial time.
func ParseResolverAddress(s string) (ResolverAddress, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return ResolverAddress{}, fmt.Errorf("resolver address is empty")
	}
	scheme := ""
	rest := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = strings.ToLower(raw[:i])
		rest = raw[i+3:]
	}
	if rest == "" {
		return ResolverAddress{}, fmt.Errorf("resolver %q has no address", raw)
	}
	// A userinfo or fragment is never part of a DNS endpoint; reject it rather
	// than hand sing-box a URL it will misread.
	if strings.ContainsAny(rest, "#@") {
		return ResolverAddress{}, fmt.Errorf("resolver %q must not carry a fragment or userinfo", raw)
	}

	type proto struct {
		typ        string
		defaultPrt int
		hasPath    bool
	}
	protos := map[string]proto{
		"":      {typ: "udp", defaultPrt: 53},
		"udp":   {typ: "udp", defaultPrt: 53},
		"tcp":   {typ: "tcp", defaultPrt: 53},
		"tls":   {typ: "tls", defaultPrt: 853},
		"dot":   {typ: "tls", defaultPrt: 853},
		"https": {typ: "https", defaultPrt: 443, hasPath: true},
		"doh":   {typ: "https", defaultPrt: 443, hasPath: true},
	}
	p, ok := protos[scheme]
	if !ok {
		return ResolverAddress{}, fmt.Errorf("resolver scheme %q is not supported (use tcp, udp, tls or https)", scheme)
	}

	hostport := rest
	path := ""
	if p.hasPath {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			hostport, path = rest[:i], rest[i:]
		}
	} else if strings.ContainsRune(rest, '/') {
		return ResolverAddress{}, fmt.Errorf("resolver %q must not have a path", raw)
	}

	host, portStr, err := splitHostPortDefault(hostport, p.defaultPrt)
	if err != nil {
		return ResolverAddress{}, fmt.Errorf("resolver %q: %w", raw, err)
	}
	if strings.ContainsAny(host, " \t") {
		return ResolverAddress{}, fmt.Errorf("resolver %q has an invalid host", raw)
	}
	if path == "/" {
		path = ""
	}
	return ResolverAddress{Type: p.typ, Host: host, Port: portStr, Path: path}, nil
}

// splitHostPortDefault splits "host[:port]" and applies def when the port is
// absent, handling a bracketed IPv6 literal.
func splitHostPortDefault(s string, def int) (string, int, error) {
	if s == "" {
		return "", 0, fmt.Errorf("empty address")
	}
	if strings.HasPrefix(s, "[") { // [v6]:port or [v6]
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			host = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
			if net.ParseIP(host) == nil {
				return "", 0, fmt.Errorf("%q is not a valid address", s)
			}
			return host, def, nil
		}
		p, err := validPort(port)
		if err != nil {
			return "", 0, err
		}
		return host, p, nil
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s, "::") {
		host, port := s[:i], s[i+1:]
		if host == "" {
			return "", 0, fmt.Errorf("%q has no host", s)
		}
		p, err := validPort(port)
		if err != nil {
			return "", 0, err
		}
		return host, p, nil
	}
	return s, def, nil
}

func validPort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("port %q is not in 1..65535", s)
	}
	return p, nil
}

// NormalizeDNSPattern canonicalises a nameserver-policy key into the suffix it
// matches. Clash writes suffix matches with a leading "+." (also "*.", ".",
// or nothing), and its policy keys may also be a rule-set reference
// ("geosite:cn", "rule-set:…"), which vender cannot match against a node
// hostname. The second result is false for a key that is not a plain domain,
// so it is dropped from the applied policy (it stays visible in Raw).
func NormalizeDNSPattern(pattern string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(pattern, ".")))
	if p == "" {
		return "", false
	}
	if i := strings.IndexByte(p, ':'); i >= 0 {
		// geosite:cn / rule-set:x / geoip:… are category keys, not domains.
		return "", false
	}
	for _, prefix := range []string{"+.", "*.", "."} {
		if strings.HasPrefix(p, prefix) {
			p = p[len(prefix):]
			break
		}
	}
	p = strings.Trim(p, ".")
	if p == "" || strings.ContainsAny(p, " \t") {
		return "", false
	}
	return p, true
}

// Normalize trims and canonicalises a captured policy: patterns are
// normalized into suffixes, duplicates keep their first position, and an empty
// policy collapses to nil so it is not written to profiles.yaml.
func (d *ProfileDNS) Normalize() {
	if d == nil {
		return
	}
	out := make([]DNSNamespacePolicy, 0, len(d.NamespacePolicy))
	seen := make(map[string]bool, len(d.NamespacePolicy))
	for _, e := range d.NamespacePolicy {
		domain, ok := NormalizeDNSPattern(e.Domain)
		if !ok {
			continue
		}
		server := strings.TrimSpace(e.Server)
		if server == "" {
			continue
		}
		if seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, DNSNamespacePolicy{Domain: domain, Server: server})
	}
	d.NamespacePolicy = out
	d.Nameservers = trimList(d.Nameservers)
	d.Fallback = trimList(d.Fallback)
	d.Raw = strings.TrimRight(d.Raw, "\n")
	if d.IsEmpty() {
		*d = ProfileDNS{}
	}
}

// IsEmpty reports whether the policy carries nothing worth storing.
func (d *ProfileDNS) IsEmpty() bool {
	return d == nil || (len(d.NamespacePolicy) == 0 && len(d.Nameservers) == 0 && len(d.Fallback) == 0 && d.Raw == "")
}

// Applied reports whether the policy actually changes how node hostnames are
// resolved (i.e. it has at least one nameserver-policy entry).
func (d *ProfileDNS) Applied() bool { return d != nil && len(d.NamespacePolicy) > 0 }

// Clone returns a deep copy.
func (d *ProfileDNS) Clone() *ProfileDNS {
	if d == nil {
		return nil
	}
	cp := *d
	cp.NamespacePolicy = append([]DNSNamespacePolicy(nil), d.NamespacePolicy...)
	cp.Nameservers = append([]string(nil), d.Nameservers...)
	cp.Fallback = append([]string(nil), d.Fallback...)
	return &cp
}

// Match returns the resolver a hostname should be resolved by: the entry whose
// domain is the longest suffix of host (the domain itself or a subdomain), or
// "" when no entry applies. An exact match beats a shorter suffix, so a policy
// can carve an exception out of a broader one.
func (d *ProfileDNS) Match(host string) string {
	if d == nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return ""
	}
	best, bestLen := "", -1
	for _, e := range d.NamespacePolicy {
		if e.Domain == "" || (h != e.Domain && !strings.HasSuffix(h, "."+e.Domain)) {
			continue
		}
		if len(e.Domain) > bestLen {
			best, bestLen = e.Server, len(e.Domain)
		}
	}
	return best
}

// Validate rejects a policy that cannot be rendered: an empty domain or server,
// a server string that is not a resolver address, or a non-domain entry (which
// Normalize would have dropped).
func (d *ProfileDNS) Validate() error {
	if d == nil {
		return nil
	}
	var problems []string
	for i, e := range d.NamespacePolicy {
		domain := strings.TrimSpace(e.Domain)
		if domain == "" {
			problems = append(problems, fmt.Sprintf("namespace_policy #%d has no domain", i+1))
			continue
		}
		if _, ok := NormalizeDNSPattern(domain); !ok {
			problems = append(problems, fmt.Sprintf("namespace_policy #%d domain %q is not a domain", i+1, e.Domain))
		} else if norm, _ := NormalizeDNSPattern(domain); norm != domain {
			problems = append(problems, fmt.Sprintf("namespace_policy #%d domain %q is not normalized (want %q)", i+1, e.Domain, norm))
		}
		if strings.TrimSpace(e.Server) == "" {
			problems = append(problems, fmt.Sprintf("namespace_policy #%d (%s) has no server", i+1, domain))
			continue
		}
		if _, err := ParseResolverAddress(e.Server); err != nil {
			problems = append(problems, fmt.Sprintf("namespace_policy #%d (%s): %v", i+1, domain, err))
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Subject: ProfilesFileName, Problems: problems}
	}
	return nil
}

// ResolverAddresses returns the distinct resolver addresses the policy uses,
// in first-appearance order. It is what the renderer turns into dns.servers.
func (d *ProfileDNS) ResolverAddresses() []string {
	if d == nil {
		return nil
	}
	seen := make(map[string]bool, len(d.NamespacePolicy))
	var out []string
	for _, e := range d.NamespacePolicy {
		addr := strings.TrimSpace(e.Server)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out
}

// SetNamespacePolicy upserts one domain→server mapping, replacing an existing
// entry for the same domain and keeping the rest in order.
func (d *ProfileDNS) SetNamespacePolicy(domain, server string) error {
	norm, ok := NormalizeDNSPattern(domain)
	if !ok {
		return fmt.Errorf("%q is not a domain (a bare name, or a +./*./. suffix pattern)", domain)
	}
	server = strings.TrimSpace(server)
	if _, err := ParseResolverAddress(server); err != nil {
		return err
	}
	for i := range d.NamespacePolicy {
		if d.NamespacePolicy[i].Domain == norm {
			d.NamespacePolicy[i].Server = server
			return nil
		}
	}
	d.NamespacePolicy = append(d.NamespacePolicy, DNSNamespacePolicy{Domain: norm, Server: server})
	return nil
}

// trimList trims, drops empties and de-duplicates while keeping order.
func trimList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// SortedServers is a display helper: the policy's distinct resolver addresses,
// sorted, so `profiles show` output is deterministic.
func (d *ProfileDNS) SortedServers() []string {
	out := d.ResolverAddresses()
	sort.Strings(out)
	return out
}
