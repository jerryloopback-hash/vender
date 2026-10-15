package rules

import (
	"fmt"
	"strings"
)

// Summary renders a rule's matchers as one human-readable line, e.g.
//
//	rule_set=[geosite-cn geoip-cn]
//	ip_is_private
//
// It is what `vender rules list` prints and what the TUI will print, so the
// description of a rule lives next to the rule instead of in the CLI.
func (r Rule) Summary() string {
	var parts []string
	if len(r.RuleSets) > 0 {
		parts = append(parts, "rule_set=["+strings.Join(r.RuleSets, " ")+"]")
	}
	if len(r.Domains) > 0 {
		parts = append(parts, "domain=["+strings.Join(r.Domains, " ")+"]")
	}
	if len(r.DomainSuffixes) > 0 {
		parts = append(parts, "domain_suffix=["+strings.Join(r.DomainSuffixes, " ")+"]")
	}
	if len(r.DomainKeywords) > 0 {
		parts = append(parts, "domain_keyword=["+strings.Join(r.DomainKeywords, " ")+"]")
	}
	if r.IPIsPrivate {
		parts = append(parts, "ip_is_private")
	}
	if len(r.IPCIDRs) > 0 {
		parts = append(parts, "ip_cidr=["+strings.Join(r.IPCIDRs, " ")+"]")
	}
	if len(r.Ports) > 0 {
		ports := make([]string, len(r.Ports))
		for i, p := range r.Ports {
			ports[i] = fmt.Sprint(p)
		}
		parts = append(parts, "port=["+strings.Join(ports, " ")+"]")
	}
	if len(r.ProcessNames) > 0 {
		parts = append(parts, "process_name=["+strings.Join(r.ProcessNames, " ")+"]")
	}
	return strings.Join(parts, " + ")
}

// Summary renders an action as it will appear in the generated config.
func (a Action) Summary() string {
	if a.Protocol == "" {
		return string(a.Kind)
	}
	return fmt.Sprintf("protocol=%s -> %s", a.Protocol, a.Kind)
}
