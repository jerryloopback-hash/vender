package core

import (
	"fmt"
	"net"
	"strings"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

// profileDNSPlan decides, for the servers this run renders, which resolver each
// one's *own* hostname must be resolved by — and builds the dns.servers entries
// those resolvers need.
//
// The distinction that matters: this is the engine dialling an outbound (the
// node's `server` address), not a client asking for a name. A client query goes
// through the split DNS of internal/rules and, for anything proxied, is
// answered from FakeIP without touching the network. A node hostname is
// resolved by the engine itself, at dial time, from the outbound's
// `domain_resolver`; vender points that at the profile's captured resolver so
// the provider's own DNS answers (public DNS returns a decoy for these
// domains). No policy, no plan entries, and the config is byte-identical to M7.
type profileDNSPlan struct {
	policy map[string]*config.ProfileDNS
	iface  string
	byAddr map[string]string
	order  []string
}

func newProfileDNSPlan(rc RuntimeConfig) *profileDNSPlan {
	return &profileDNSPlan{
		policy: rc.ProfileDNS,
		iface:  rc.Config.Engine.BindInterface,
		byAddr: make(map[string]string),
	}
}

// renderedServers lists the servers this run emits as outbounds, in render
// order: the selected one, or the whole auto-mode pool, and none in direct
// mode (which has no proxy outbound).
func (rc RuntimeConfig) renderedServers() []*config.Server {
	switch {
	case rc.Config.Proxy.Mode == config.ModeDirect:
		return nil
	case rc.Config.IsAutoServer():
		out := make([]*config.Server, 0, len(rc.Servers))
		for i := range rc.Servers {
			out = append(out, &rc.Servers[i])
		}
		return out
	case rc.Server != nil:
		return []*config.Server{rc.Server}
	}
	return nil
}

// resolverFor returns the tag of the resolver that must answer for srv's
// hostname, assigning the tag on first use. It is idempotent, so calling it
// once to fix the order and again while building the outbound yields the same
// tag. An empty result means the built-in resolver applies (route
// default_domain_resolver), which is the M7 behaviour.
func (p *profileDNSPlan) resolverFor(srv *config.Server) (string, error) {
	if p == nil || srv == nil || len(p.policy) == 0 {
		return "", nil
	}
	policy := p.policy[strings.ToLower(srv.ProfileName())]
	if policy == nil {
		return "", nil
	}
	addr := policy.Match(srv.Address)
	if addr == "" {
		return "", nil
	}
	if tag, ok := p.byAddr[addr]; ok {
		return tag, nil
	}
	if _, err := config.ParseResolverAddress(addr); err != nil {
		return "", fmt.Errorf("core: profile %q DNS resolver %q: %w", srv.ProfileName(), addr, err)
	}
	tag := fmt.Sprintf("%s%d", profileDNSTagPrefix, len(p.order)+1)
	p.byAddr[addr] = tag
	p.order = append(p.order, addr)
	return tag, nil
}

// applied reports whether the plan changed anything, i.e. whether any rendered
// node has a profile resolver.
func (p *profileDNSPlan) applied() bool { return p != nil && len(p.order) > 0 }

// entries is what the plan adds to `dns.servers`: the local bootstrap and then
// the profile resolvers, in first-use order. It is nil when the plan is empty,
// which is what keeps a config without a policy byte-identical.
//
// The bootstrap is emitted whenever the plan is applied — even for an
// IP-literal resolver — because vender also points the direct outbound at it in
// global mode, where the extra resolvers would otherwise leave a dial field
// without a resolver (sing-box 1.14 rejects that).
func (p *profileDNSPlan) entries() []rules.DNSServer {
	if !p.applied() {
		return nil
	}
	out := []rules.DNSServer{{Tag: tagDNSSystem, Type: "local"}}
	for _, addr := range p.order {
		ra, err := config.ParseResolverAddress(addr)
		if err != nil {
			// resolverFor already rejected it; keep the renderer total.
			continue
		}
		entry := rules.DNSServer{
			Tag:    p.byAddr[addr],
			Type:   ra.Type,
			Server: ra.Host,
			Port:   ra.Port,
			Path:   ra.Path,
			// Pin the resolver's own dial the way the outbound is pinned:
			// on a multi-homed or TUN host it must leave by the same
			// interface, or it would be captured by the tunnel it is meant
			// to avoid.
			BindInterface: p.iface,
		}
		if net.ParseIP(ra.Host) == nil {
			entry.DomainResolver = tagDNSSystem
		}
		out = append(out, entry)
	}
	return out
}

// domainResolverRef is a `domain_resolver` reference to a DNS server tag, or
// nil when the built-in resolver applies. It is the one shape shared by an
// outbound's dial fields and a DNS server's own bootstrap.
func domainResolverRef(tag string) *singboxDomainResolver {
	if tag == "" {
		return nil
	}
	return &singboxDomainResolver{Server: tag}
}
