package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

// Outbound tags used in the generated sing-box configuration. They are part of
// vender's contract with its own rules: internal/rules speaks in terms of
// "proxy", "direct" and "block", and render.go is the single place that binds
// those names to concrete outbound definitions.
const (
	tagProxy  = "proxy"
	tagDirect = "direct"
	tagBlock  = "block"
	// tagMixed is the tag of the local mixed SOCKS5+HTTP inbound.
	tagMixed = "mixed-in"
)

// singboxConfig is the subset of sing-box's configuration schema that vender
// generates. Fields are ordered to match the documented sing-box structure so
// the rendered file reads top-down. Unknown-to-vender sing-box options are
// simply never emitted.
type singboxConfig struct {
	Log          singboxLog           `json:"log"`
	Experimental *singboxExperimental `json:"experimental,omitempty"`
	DNS          *singboxDNS          `json:"dns,omitempty"`
	Inbounds     []singboxInbound     `json:"inbounds"`
	Outbounds    []any                `json:"outbounds"`
	Route        singboxRoute         `json:"route"`
}

// singboxExperimental carries sing-box's out-of-band runtime services. vender
// emits it only when the daemon asked for them (M3: the Clash API, which is
// how the TUI gets live traffic counters and latency).
type singboxExperimental struct {
	ClashAPI *singboxClashAPI `json:"clash_api,omitempty"`
}

// singboxClashAPI is sing-box's Clash-compatible REST/streaming API. It is
// bound to loopback only and always carries a secret: without one,
// sing-box rejects the config, and an unauthenticated local API would let any
// local process reconfigure the running engine.
type singboxClashAPI struct {
	ExternalController string `json:"external_controller"`
	Secret             string `json:"secret,omitempty"`
}

// singboxDNS is the generated `dns` section. vender emits it only for the
// modes that have a proxy to resolve through; in direct mode there is nothing
// to protect and sing-box's own default (the system resolver) is right.
type singboxDNS struct {
	Servers  []singboxDNSServer `json:"servers"`
	Rules    []singboxDNSRule   `json:"rules,omitempty"`
	Final    string             `json:"final,omitempty"`
	Strategy string             `json:"strategy,omitempty"`
}

type singboxDNSServer struct {
	Tag    string `json:"tag"`
	Type   string `json:"type"`
	Server string `json:"server"`
	// Detour is the outbound the query leaves through. It is omitted for the
	// domestic resolver, which is reached directly; sing-box 1.14 rejects a
	// detour to the empty `direct` outbound vender otherwise emits.
	Detour string `json:"detour,omitempty"`
}

type singboxDNSRule struct {
	RuleSet []string `json:"rule_set"`
	Server  string   `json:"server"`
}

type singboxLog struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

type singboxInbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
}

type singboxRoute struct {
	// RuleSet declares the embedded rule set files sing-box may match against.
	RuleSet []singboxRuleSet `json:"rule_set,omitempty"`
	// DefaultDomainResolver resolves a destination name when a rule needs its
	// address (ip_is_private, geoip-cn) or when a direct outbound dials a name.
	DefaultDomainResolver *singboxDomainResolver `json:"default_domain_resolver,omitempty"`
	Rules                 []singboxRule          `json:"rules"`
	Final                 string                 `json:"final"`
}

// singboxRuleSet points sing-box at one rule set file extracted from the
// vender binary.
type singboxRuleSet struct {
	Type   string `json:"type"`   // always "local"
	Tag    string `json:"tag"`    // referenced by rules and DNS rules
	Format string `json:"format"` // always "binary" (SRS)
	Path   string `json:"path"`   // absolute, inside the run dir
}

type singboxDomainResolver struct {
	Server string `json:"server"`
}

// singboxRule is the generated form of one route rule. A rule is either an
// action (no matchers, no outbound), or a matcher list plus an outbound; the
// zero omitempty tags keep the two shapes from bleeding into each other.
type singboxRule struct {
	// Action turns the entry into a route action (sniff, hijack-dns) instead
	// of an outbound selection.
	Action string `json:"action,omitempty"`
	// Protocol restricts an action to a sniffed protocol.
	Protocol []string `json:"protocol,omitempty"`
	// RuleSet matches destinations covered by the named rule sets.
	RuleSet       []string `json:"rule_set,omitempty"`
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	// IPIsPrivate matches private/loopback/link-local destination addresses.
	IPIsPrivate bool     `json:"ip_is_private,omitempty"`
	Port        []int    `json:"port,omitempty"`
	ProcessName []string `json:"process_name,omitempty"`
	// Outbound is the tag the connection is routed to.
	Outbound string `json:"outbound,omitempty"`
}

// --- outbound dialects -----------------------------------------------------

type vlessOutbound struct {
	Type          string     `json:"type"`
	Tag           string     `json:"tag"`
	Server        string     `json:"server"`
	ServerPort    int        `json:"server_port"`
	UUID          string     `json:"uuid"`
	Flow          string     `json:"flow,omitempty"`
	BindInterface string     `json:"bind_interface,omitempty"`
	TLS           *tlsConfig `json:"tls,omitempty"`
}

type hysteria2Outbound struct {
	Type          string     `json:"type"`
	Tag           string     `json:"tag"`
	Server        string     `json:"server"`
	ServerPort    int        `json:"server_port"`
	Password      string     `json:"password"`
	Obfs          *hy2Obfs   `json:"obfs,omitempty"`
	BindInterface string     `json:"bind_interface,omitempty"`
	TLS           *tlsConfig `json:"tls,omitempty"`
}

type hy2Obfs struct {
	Type     string `json:"type"`
	Password string `json:"password,omitempty"`
}

type plainOutbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type tlsConfig struct {
	Enabled                    bool         `json:"enabled"`
	ServerName                 string       `json:"server_name,omitempty"`
	Insecure                   bool         `json:"insecure,omitempty"`
	ALPN                       []string     `json:"alpn,omitempty"`
	UTLS                       *utlsConfig  `json:"utls,omitempty"`
	Reality                    *realityOpts `json:"reality,omitempty"`
	CertificatePublicKeySHA256 []string     `json:"certificate_public_key_sha256,omitempty"`
}

type utlsConfig struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type realityOpts struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id,omitempty"`
}

// BuildConfig renders rc into the sing-box configuration vender runs. It is
// pure: the result is a deterministic JSON document, which is what the golden
// tests compare against. (The one ambient input is the run directory, which
// vender always resolves to an absolute path and which the golden cases pass
// as an absolute path.)
func BuildConfig(rc RuntimeConfig) ([]byte, error) {
	if err := rc.Validate(); err != nil {
		return nil, err
	}
	host, port, err := splitListen(rc.Config.Proxy.Listen)
	if err != nil {
		return nil, err
	}

	cfg := singboxConfig{
		Log: singboxLog{Level: rc.Config.LogLevel, Timestamp: true},
		Inbounds: []singboxInbound{{
			Type:       "mixed",
			Tag:        tagMixed,
			Listen:     host,
			ListenPort: port,
		}},
	}

	// The Clash API is opt-in: only the daemon asks for it, and only so the TUI
	// can read live traffic and measure latency. The address is validated by
	// RuntimeConfig.Validate (loopback only, secret required).
	if rc.ClashListen != "" {
		cfg.Experimental = &singboxExperimental{
			ClashAPI: &singboxClashAPI{
				ExternalController: rc.ClashListen,
				Secret:             rc.ClashSecret,
			},
		}
	}

	// sing-box 1.13 removed the legacy inbound `sniff` option outright, so
	// vender never emits it; sniffing is a route action ({action: sniff}),
	// emitted below in rule mode.

	direct := plainOutbound{Type: "direct", Tag: tagDirect}
	block := plainOutbound{Type: "block", Tag: tagBlock}

	final := tagDirect
	if rc.Config.Proxy.Mode == config.ModeDirect {
		// Direct mode does not even reference a proxy outbound, so an
		// unusable selected server cannot break "direct".
		cfg.Outbounds = []any{direct, block}
	} else {
		proxy, err := buildOutbound(rc)
		if err != nil {
			return nil, err
		}
		cfg.Outbounds = []any{proxy, direct, block}
		final = tagProxy
	}

	// Rule mode is the only mode that ships the built-in policy; global means
	// what it says (everything through the proxy), so emitting CN-direct rules
	// there would silently break the mode's contract.
	switch rc.Config.Proxy.Mode {
	case config.ModeRule:
		dns, err := buildDNS(rules.BuiltinDNS())
		if err != nil {
			return nil, err
		}
		cfg.DNS = dns

		sets, err := buildRuleSets(rc.RunDir, rules.ReferencedRuleSets())
		if err != nil {
			return nil, err
		}
		routeRules, err := buildRules()
		if err != nil {
			return nil, err
		}
		cfg.Route = singboxRoute{
			RuleSet: sets,
			// Destination names are resolved with the domestic resolver: it is
			// the one that can answer for the names a direct outbound dials,
			// and a proxied outbound never resolves locally at all (it passes
			// the name to the server), so this cannot leak a proxied lookup.
			DefaultDomainResolver: &singboxDomainResolver{Server: rules.DNSCN},
			Rules:                 routeRules,
			Final:                 final,
		}

	case config.ModeGlobal:
		dns, err := buildDNS(rules.GlobalDNS())
		if err != nil {
			return nil, err
		}
		cfg.DNS = dns
		cfg.Route = singboxRoute{Rules: []singboxRule{}, Final: final}

	default: // config.ModeDirect
		cfg.Route = singboxRoute{Rules: []singboxRule{}, Final: final}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("core: encode sing-box config: %w", err)
	}
	return append(data, '\n'), nil
}

// buildOutbound renders the selected server as the proxy outbound.
func buildOutbound(rc RuntimeConfig) (any, error) {
	srv := rc.Server
	if srv == nil {
		return nil, errors.New("core: proxy mode requires a selected server")
	}
	iface := rc.Config.Engine.BindInterface

	switch srv.Type {
	case config.TypeVLESS:
		out := vlessOutbound{
			Type:          "vless",
			Tag:           tagProxy,
			Server:        srv.Address,
			ServerPort:    srv.Port,
			UUID:          srv.UUID,
			Flow:          srv.Flow,
			BindInterface: iface,
		}
		tls, err := vlessTLS(srv)
		if err != nil {
			return nil, err
		}
		out.TLS = tls
		return out, nil

	case config.TypeHysteria2:
		out := hysteria2Outbound{
			Type:          "hysteria2",
			Tag:           tagProxy,
			Server:        srv.Address,
			ServerPort:    srv.Port,
			Password:      srv.Password,
			BindInterface: iface,
			TLS: &tlsConfig{
				Enabled:                    true,
				ServerName:                 srv.SNI,
				Insecure:                   srv.Insecure,
				ALPN:                       srv.ALPN,
				CertificatePublicKeySHA256: nonEmptySlice(srv.PinSHA256),
			},
		}
		if srv.Obfs != "" {
			out.Obfs = &hy2Obfs{Type: srv.Obfs, Password: srv.ObfsPassword}
		}
		return out, nil

	default:
		return nil, fmt.Errorf("core: unsupported server type %q", srv.Type)
	}
}

// vlessTLS renders the TLS block for a VLESS server, or nil for security=none.
func vlessTLS(srv *config.Server) (*tlsConfig, error) {
	switch srv.Security {
	case "", config.SecurityNone:
		if srv.Flow != "" {
			return nil, errors.New("core: vless flow requires security tls or reality")
		}
		return nil, nil

	case config.SecurityTLS:
		return &tlsConfig{
			Enabled:    true,
			ServerName: srv.SNI,
			UTLS:       utls(srv.Fingerprint),
		}, nil

	case config.SecurityReality:
		return &tlsConfig{
			Enabled:    true,
			ServerName: srv.SNI,
			UTLS:       utls(srv.Fingerprint),
			Reality: &realityOpts{
				Enabled:   true,
				PublicKey: srv.PublicKey,
				ShortID:   srv.ShortID,
			},
		}, nil

	default:
		return nil, fmt.Errorf("core: unsupported vless security %q", srv.Security)
	}
}

func utls(fingerprint string) *utlsConfig {
	if fingerprint == "" {
		return nil
	}
	return &utlsConfig{Enabled: true, Fingerprint: fingerprint}
}

// buildRuleSets declares the embedded rule sets sing-box may match against, in
// the order the caller lists them. Paths are absolute and inside the run dir,
// where core.WriteConfig has extracted the embedded .srs files.
func buildRuleSets(runDir string, tags []string) ([]singboxRuleSet, error) {
	sets := make([]singboxRuleSet, 0, len(tags))
	for _, tag := range tags {
		path, err := rules.RuleSetPath(runDir, tag)
		if err != nil {
			return nil, err
		}
		sets = append(sets, singboxRuleSet{
			Type:   "local",
			Tag:    tag,
			Format: "binary",
			Path:   path,
		})
	}
	return sets, nil
}

// buildDNS converts vender's DNS policy into sing-box's `dns` section.
func buildDNS(policy rules.DNS) (*singboxDNS, error) {
	out := &singboxDNS{
		Servers:  make([]singboxDNSServer, 0, len(policy.Servers)),
		Final:    policy.Final,
		Strategy: policy.Strategy,
	}
	for _, s := range policy.Servers {
		if s.Tag == "" || s.Server == "" {
			return nil, fmt.Errorf("core: DNS server %q is incomplete", s.Tag)
		}
		out.Servers = append(out.Servers, singboxDNSServer{
			Tag:    s.Tag,
			Type:   s.Type,
			Server: s.Server,
			Detour: s.Detour,
		})
	}
	for _, r := range policy.Rules {
		if len(r.RuleSets) == 0 {
			return nil, fmt.Errorf("core: DNS rule %q matches no rule set", r.Name)
		}
		out.Rules = append(out.Rules, singboxDNSRule{RuleSet: r.RuleSets, Server: r.Server})
	}
	return out, nil
}

// buildRules converts the built-in policy into sing-box route rules: first the
// actions (sniff, hijack-dns), then the destination rules in evaluation order.
//
// DNS-only rules are still skipped: sing-box expresses DNS policy as the
// `dns` section plus the hijack-dns action, not as destination matches, so a
// DNSOnly rule here would silently misroute DNS. BuiltinRules returns none.
func buildRules() ([]singboxRule, error) {
	out := make([]singboxRule, 0)
	for _, a := range rules.BuiltinActions() {
		rule := singboxRule{Action: string(a.Kind)}
		if a.Protocol != "" {
			rule.Protocol = []string{a.Protocol}
		}
		out = append(out, rule)
	}
	for _, r := range rules.BuiltinRules() {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if r.DNSOnly {
			continue
		}
		tag, err := outboundTag(r.Outbound)
		if err != nil {
			return nil, err
		}
		out = append(out, singboxRule{
			RuleSet:       r.RuleSets,
			Domain:        r.Domains,
			DomainSuffix:  r.DomainSuffixes,
			DomainKeyword: r.DomainKeywords,
			IPCIDR:        r.IPCIDRs,
			IPIsPrivate:   r.IPIsPrivate,
			Port:          r.Ports,
			ProcessName:   r.ProcessNames,
			Outbound:      tag,
		})
	}
	return out, nil
}

func outboundTag(o rules.Outbound) (string, error) {
	switch o {
	case rules.OutProxy:
		return tagProxy, nil
	case rules.OutDirect:
		return tagDirect, nil
	case rules.OutBlock:
		return tagBlock, nil
	default:
		return "", fmt.Errorf("core: rule outbound %q has no sing-box tag", o)
	}
}

// validateClashListen rejects a Clash API address that would expose the API
// beyond loopback. sing-box's Clash API is powerful (it can switch the active
// outbound) and vender only ever needs it locally, so a wildcard or external
// address is treated as a configuration mistake rather than accepted.
func validateClashListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("core: clash api address %q must be host:port: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if host != "localhost" {
			return fmt.Errorf("core: clash api address %q must use a loopback host, not a name", addr)
		}
	} else if !ip.IsLoopback() {
		return fmt.Errorf("core: clash api address %q must be a loopback address", addr)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("core: clash api address %q has an invalid port", addr)
	}
	return nil
}

// splitListen splits a validated "host:port" listen address into sing-box's
// separate listen / listen_port fields.
func splitListen(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("core: listen address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("core: listen address %q has an invalid port", addr)
	}
	return host, port, nil
}

func nonEmptySlice(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
