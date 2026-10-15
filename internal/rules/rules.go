// Package rules will supply vender's built-in routing rules and translate them
// into sing-box route rules (M2).
//
// M0 ships the data model and the BuiltinRules entry point only: the rule sets
// themselves (CN direct, ad blocking, private IP direct, ...) land in M2
// together with the "rule" proxy mode. Keeping the types here, next to the
// contract, lets M1 render an empty route section and M2 fill it without
// touching the engine.
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

// Rule is one routing rule, expressed in vender's own terms. M1/M2 convert it
// to sing-box's route rule JSON (domain_suffix, ip_cidr, port, process_name...).
type Rule struct {
	// Name is a human-readable identifier, e.g. "private-ips".
	Name string
	// Outbound is where matches are sent.
	Outbound Outbound
	// Domains matches exact domain names.
	Domains []string
	// DomainSuffixes matches suffixes, e.g. "cn" matches "example.cn".
	DomainSuffixes []string
	// DomainKeywords matches substrings anywhere in the domain.
	DomainKeywords []string
	// IPCIDRs matches destination IP ranges.
	IPCIDRs []string
	// Ports matches destination ports.
	Ports []int
	// ProcessNames matches the local process name (Linux) / executable (Windows).
	ProcessNames []string
	// DNSOnly marks a rule used only during DNS resolution.
	DNSOnly bool
}

// Validate checks that a rule actually says something.
func (r Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rules: rule name must not be empty")
	}
	switch r.Outbound {
	case OutDirect, OutProxy, OutBlock:
	default:
		return fmt.Errorf("rules: rule %q has unknown outbound %q", r.Name, r.Outbound)
	}
	if len(r.Domains)+len(r.DomainSuffixes)+len(r.DomainKeywords)+len(r.IPCIDRs)+len(r.Ports)+len(r.ProcessNames) == 0 {
		return fmt.Errorf("rules: rule %q matches nothing", r.Name)
	}
	return nil
}

// BuiltinRules returns vender's default rule set, in evaluation order.
//
// M2 implements this. M0 returns nil so that `proxy.mode: rule` behaves like
// "proxy everything" until the rule sets exist; callers must treat a nil result
// as "no routing rules configured" rather than as "match nothing".
func BuiltinRules() []Rule {
	return nil
}
