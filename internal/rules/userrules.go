package rules

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file implements the *critical* built-in list and the optional per-user
// rules file (rules.yaml).
//
// Both exist for the same reason: the built-in policy must never be able to cut
// the user off from the machine's own control plane. The critical list is a
// handful of domains that have to work even when the proxy is dead (an agent's
// model API); the user file is the escape hatch for everything else, evaluated
// before the built-in policy so a user entry always wins.

// criticalDirectSuffixes are the domains that must be routed directly no matter
// what — ahead of the built-in policy, ahead of the user's own rules, and
// resolved by the domestic resolver rather than the fake-IP one. Keep this list
// tiny: every entry is a hole in "everything foreign goes through the proxy".
//
// The rule of thumb for adding one is two conditions, both required:
//
//	(a) losing it would make the machine unmanageable (not merely
//	    inconvenient) while the proxy is unreachable, AND
//	(b) it is reliably reachable *directly* on this network.
//
// api.tavily.com was listed here and fails (b): the direct path to it times out
// on this network while the proxy reaches it fine, so forcing it direct broke
// it rather than protecting it. It now follows the normal policy (built-in
// policy -> final: proxy) and remains overridable from rules.yaml.
var criticalDirectSuffixes = []string{
	"opencode.ai", // the coding agent's model/API plane
}

// CriticalDirectSuffixes returns the built-in "must survive a dead proxy" list.
// The result is a copy; callers must not mutate it.
func CriticalDirectSuffixes() []string {
	out := make([]string, len(criticalDirectSuffixes))
	copy(out, criticalDirectSuffixes)
	return out
}

// CriticalRules returns the built-in critical list as a single direct rule. It
// is evaluated before every other destination rule (built-in or user).
func CriticalRules() []Rule {
	return []Rule{{
		Name:           "critical-direct",
		Outbound:       OutDirect,
		DomainSuffixes: CriticalDirectSuffixes(),
	}}
}

// UserList is one of the three ordered lists in rules.yaml. Entries are
// strings, each one of:
//
//	example.com        the domain and every subdomain (domain_suffix)
//	.cn                a TLD suffix
//	=example.com       exactly this domain (no subdomains)
//	10.0.0.0/8         an IP range (ip_cidr)
//
// The explicit prefixes domain:, suffix: and cidr: are also accepted, for when
// a bare entry would be ambiguous.
type UserList []string

// UserRules is the parsed contents of rules.yaml. Every list is optional; a
// missing file parses to an empty (but non-nil) value.
type UserRules struct {
	// Direct domains are forced out directly, ahead of the built-in policy.
	Direct UserList `yaml:"direct,omitempty"`
	// Proxy domains are forced through the proxy, ahead of the built-in policy.
	Proxy UserList `yaml:"proxy,omitempty"`
	// Block domains are rejected outright, ahead of the built-in policy.
	Block UserList `yaml:"block,omitempty"`
}

// Empty reports whether the file says nothing at all.
func (u *UserRules) Empty() bool {
	return u == nil || (len(u.Direct) == 0 && len(u.Proxy) == 0 && len(u.Block) == 0)
}

// ParseUserRules decodes rules.yaml. Unknown keys are ignored, so a file
// written by a newer vender still loads in an older one.
func ParseUserRules(data []byte) (*UserRules, error) {
	u := &UserRules{}
	if err := yaml.Unmarshal(data, u); err != nil {
		return nil, fmt.Errorf("rules.yaml: parse yaml: %w", err)
	}
	return u, nil
}

// Validate checks every entry parses and reports all problems at once.
func (u *UserRules) Validate() error {
	if u == nil {
		return nil
	}
	var problems []string
	for _, l := range []struct {
		name    string
		entries UserList
	}{
		{"direct", u.Direct},
		{"proxy", u.Proxy},
		{"block", u.Block},
	} {
		for _, e := range l.entries {
			if _, err := parseEntry(e); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", l.name, err))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("rules.yaml: %s", strings.Join(problems, "; "))
	}
	return nil
}

// match is the sing-box-side interpretation of one user list.
type match struct {
	Domains  []string
	Suffixes []string
	CIDRs    []string
}

// compile turns a list of entries into the matchers sing-box understands.
func (l UserList) compile() (match, error) {
	var m match
	for _, e := range l {
		spec, err := parseEntry(e)
		if err != nil {
			return match{}, err
		}
		m.Domains = append(m.Domains, spec.Domains...)
		m.Suffixes = append(m.Suffixes, spec.Suffixes...)
		m.CIDRs = append(m.CIDRs, spec.CIDRs...)
	}
	return m, nil
}

// Rules returns the user policy as ordered routing rules: direct, then proxy,
// then block. Empty lists are skipped, so an empty file yields no rules. The
// order within the file is what makes "direct wins over proxy" true when a
// domain appears in both.
func (u *UserRules) Rules() ([]Rule, error) {
	if u == nil {
		return nil, nil
	}
	var out []Rule
	for _, l := range []struct {
		name     string
		entries  UserList
		outbound Outbound
	}{
		{"user-direct", u.Direct, OutDirect},
		{"user-proxy", u.Proxy, OutProxy},
		{"user-block", u.Block, OutBlock},
	} {
		if len(l.entries) == 0 {
			continue
		}
		m, err := l.entries.compile()
		if err != nil {
			return nil, err
		}
		out = append(out, Rule{
			Name:           l.name,
			Outbound:       l.outbound,
			Domains:        m.Domains,
			DomainSuffixes: m.Suffixes,
			IPCIDRs:        m.CIDRs,
		})
	}
	return out, nil
}

// DirectSuffixes is the domain-side of the direct list, i.e. what the DNS
// policy can mirror (CIDRs are not matchable in a DNS rule in sing-box 1.14
// without response matching, and do not need to be: a direct outbound that
// dials a *name* re-resolves it through the domestic resolver anyway).
func (u *UserRules) DirectSuffixes() (domains, suffixes []string) {
	if u == nil {
		return nil, nil
	}
	m, err := u.Direct.compile()
	if err != nil {
		return nil, nil
	}
	return m.Domains, m.Suffixes
}

// one entry's parsed form.
func parseEntry(e string) (match, error) {
	s := strings.TrimSpace(e)
	if s == "" {
		return match{}, fmt.Errorf("empty entry")
	}
	// Explicit prefixes.
	var forced string
	for _, p := range []string{"domain:", "exact:", "suffix:", "cidr:", "ip:"} {
		if v, ok := strings.CutPrefix(s, p); ok {
			s = strings.TrimSpace(v)
			switch p {
			case "domain:", "exact:":
				forced = "domain"
			case "suffix:":
				forced = "suffix"
			default:
				forced = "cidr"
			}
			break
		}
	}
	if s == "" {
		return match{}, fmt.Errorf("empty entry after prefix")
	}

	switch forced {
	case "domain":
		if err := validDomain(s); err != nil {
			return match{}, err
		}
		return match{Domains: []string{s}}, nil
	case "suffix":
		s = strings.TrimPrefix(s, ".")
		if err := validDomain(s); err != nil {
			return match{}, err
		}
		return match{Suffixes: []string{s}}, nil
	case "cidr":
		if _, _, err := net.ParseCIDR(s); err != nil {
			return match{}, fmt.Errorf("%q is not a CIDR: %v", s, err)
		}
		return match{CIDRs: []string{s}}, nil
	}

	// Bare forms.
	switch {
	case strings.HasPrefix(s, "="):
		d := strings.TrimPrefix(s, "=")
		if err := validDomain(d); err != nil {
			return match{}, err
		}
		return match{Domains: []string{d}}, nil
	case strings.ContainsAny(s, "/"):
		if _, _, err := net.ParseCIDR(s); err != nil {
			return match{}, fmt.Errorf("%q is not a CIDR: %v", s, err)
		}
		return match{CIDRs: []string{s}}, nil
	default:
		d := strings.TrimPrefix(s, ".")
		if err := validDomain(d); err != nil {
			return match{}, err
		}
		return match{Suffixes: []string{d}}, nil
	}
}

// validDomain accepts a hostname/TLD-ish string: no whitespace, no scheme, no
// path. It deliberately allows a bare TLD ("cn") and a leading dot, which a
// domain_suffix rule needs.
func validDomain(s string) error {
	if s == "" {
		return fmt.Errorf("empty domain")
	}
	if strings.ContainsAny(s, " \t/\\:@") {
		return fmt.Errorf("domain %q must not contain spaces, slashes, colons or paths", s)
	}
	return nil
}

// LoadUserRules reads rules.yaml from path. A missing file is not an error: it
// means "no user overrides" and yields (nil, nil). A present but invalid file
// is an error, so a typo is reported instead of silently ignored.
func LoadUserRules(path string) (*UserRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("rules.yaml: %w", err)
	}
	u, err := ParseUserRules(data)
	if err != nil {
		return nil, err
	}
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return u, nil
}

// WriteUserRules writes the commented template to path. It refuses to overwrite
// an existing file unless force is set, and reports whether it wrote one.
func WriteUserRules(path string, force bool) (bool, error) {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("rules.yaml: %w", err)
	}
	if err := os.WriteFile(path, []byte(RulesFileTemplate), 0o644); err != nil {
		return false, fmt.Errorf("rules.yaml: %w", err)
	}
	return true, nil
}

// RulesFileTemplate is the commented rules.yaml written by `vender rules init`
// (and used as documentation). It explains each list and every entry form.
const RulesFileTemplate = `# vender user routing rules (optional).
#
# This file is merged *before* the built-in policy, so an entry here always
# wins over the built-in lists; the built-in critical list is evaluated before
# this file and cannot be overridden.
#
# Each list holds entries, one per line. An entry is one of:
#
#   example.com        example.com and every subdomain (domain_suffix)
#   .cn                a suffix, same as above (a leading dot is allowed)
#   =example.com       exactly example.com, no subdomains
#   10.0.0.0/8         an IP range (ip_cidr)
#   suffix:example.com explicit forms, also domain: and cidr:
#
# Ordering inside each list does not matter, but the lists themselves are
# evaluated in the order direct, proxy, block: a healthy entry therefore wins
# over an unhealthy one.

# Force these out directly (never through the proxy).
direct: []

# Force these through the proxy (even if the built-in policy would send them
# out directly).
proxy: []

# Reject these outright.
block: []
`
