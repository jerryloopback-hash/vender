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
//
// tagProxy is always the outbound the policy means by "the proxy". In single
// mode it is the selected server; in auto mode (selected_server: "auto") it is
// the URLTest failover group, so every rule and the route final keep working
// without knowing which member is currently carrying traffic.
const (
	tagProxy  = "proxy"
	tagDirect = "direct"
	tagBlock  = "block"
	// tagMixed is the tag of the local mixed SOCKS5+HTTP inbound.
	tagMixed = "mixed-in"
	// tagTun is the tag of the optional TUN inbound.
	tagTun = "tun-in"
)

// Failover knobs for the URLTest group (selected_server: "auto").
//
//   - url is vender's usual "generate 204" probe, reached *through* the member,
//     so a node that connects but cannot carry traffic is judged unhealthy.
//   - interval and tolerance come from engine.failover_interval /
//     engine.failover_tolerance_ms, with the defaults in internal/config. A
//     shorter interval reacts faster to a node going bad but runs more probe
//     traffic; the probes run *through* each member. A tolerance above
//     sing-box's 50 ms default stops two nodes of similar latency from
//     flapping on every jittery sample.
const autoFailoverURL = DefaultDelayTestURL

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
	Server string `json:"server,omitempty"`
	// Detour is the outbound the query leaves through. It is omitted for the
	// domestic and FakeIP resolvers, which are local; sing-box 1.14 rejects a
	// detour to the empty `direct` outbound vender otherwise emits.
	Detour string `json:"detour,omitempty"`
	// Inet4Range/Inet6Range are the FakeIP pools (type "fakeip" only).
	Inet4Range string `json:"inet4_range,omitempty"`
	Inet6Range string `json:"inet6_range,omitempty"`
}

type singboxDNSRule struct {
	// Action is emitted explicitly ("route") even though sing-box defaults to
	// it: `server` is deprecated as a bare rule field and moving to the route
	// action now keeps the config forward-compatible.
	Action       string   `json:"action"`
	RuleSet      []string `json:"rule_set,omitempty"`
	Domain       []string `json:"domain,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	QueryType    []string `json:"query_type,omitempty"`
	Server       string   `json:"server,omitempty"`
}

type singboxLog struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

// singboxInbound is one entry of the generated `inbounds` list. The mixed and
// TUN inbounds share the type so that both render through one struct; the
// fields a given inbound does not use are omitted, which is what keeps the
// TUN-off output byte-identical to M1/M2's.
type singboxInbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`

	// --- tun only -----------------------------------------------------------
	//
	// `address` is the modern replacement for the legacy inet4_address /
	// inet6_address pair, which sing-box 1.12 removed outright (the pinned
	// 1.14.3 refuses them by name).
	InterfaceName string   `json:"interface_name,omitempty"`
	Address       []string `json:"address,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	Stack         string   `json:"stack,omitempty"`
	// Stack and the flags below are emitted only in TUN mode. `stack` is always
	// explicit so vender's behaviour does not depend on an engine default that
	// can change between sing-box releases.
	AutoRoute              bool     `json:"auto_route,omitempty"`
	StrictRoute            bool     `json:"strict_route,omitempty"`
	RouteExcludeAddress    []string `json:"route_exclude_address,omitempty"`
	RouteExcludeAddressSet []string `json:"route_exclude_address_set,omitempty"`
}

type singboxRoute struct {
	// RuleSet declares the embedded rule set files sing-box may match against.
	RuleSet []singboxRuleSet `json:"rule_set,omitempty"`
	// AutoDetectInterface binds the engine's own outbound connections to the
	// interface holding the default route. With a TUN up it is what keeps the
	// connection *to the proxy server* out of the tunnel (without it the
	// engine can route its own handshake back into its own TUN on a host whose
	// proxy server is reached by IP). Emitted only in TUN mode, and only when
	// engine.bind_interface has not already pinned the outbound.
	AutoDetectInterface bool `json:"auto_detect_interface,omitempty"`
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

// shadowsocksOutbound renders a Shadowsocks server. The method is the cipher;
// SIP003 plugins are deliberately not rendered (a link needing one is rejected
// at parse time).
type shadowsocksOutbound struct {
	Type          string `json:"type"`
	Tag           string `json:"tag"`
	Server        string `json:"server"`
	ServerPort    int    `json:"server_port"`
	Method        string `json:"method"`
	Password      string `json:"password"`
	BindInterface string `json:"bind_interface,omitempty"`
}

// vmessOutbound renders a VMess server. Security is the vmess cipher ("auto",
// "aes-128-gcm", ...), AlterID its "aid".
type vmessOutbound struct {
	Type          string          `json:"type"`
	Tag           string          `json:"tag"`
	Server        string          `json:"server"`
	ServerPort    int             `json:"server_port"`
	UUID          string          `json:"uuid"`
	Security      string          `json:"security,omitempty"`
	AlterID       int             `json:"alter_id,omitempty"`
	BindInterface string          `json:"bind_interface,omitempty"`
	TLS           *tlsConfig      `json:"tls,omitempty"`
	Transport     *v2rayTransport `json:"transport,omitempty"`
}

// trojanOutbound renders a Trojan server. TLS is always present: Trojan has no
// plaintext variant.
type trojanOutbound struct {
	Type          string          `json:"type"`
	Tag           string          `json:"tag"`
	Server        string          `json:"server"`
	ServerPort    int             `json:"server_port"`
	Password      string          `json:"password"`
	BindInterface string          `json:"bind_interface,omitempty"`
	TLS           *tlsConfig      `json:"tls,omitempty"`
	Transport     *v2rayTransport `json:"transport,omitempty"`
}

// v2rayTransport is the transport block shared by vmess and trojan. Which
// field carries the destination depends on the type: ws uses a Host header,
// http (a TCP stream with an HTTP header) a host list, grpc a service name.
type v2rayTransport struct {
	Type        string            `json:"type"`
	Path        string            `json:"path,omitempty"`
	Host        []string          `json:"host,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	ServiceName string            `json:"service_name,omitempty"`
}

type plainOutbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

// urltestOutbound is the failover group rendered for selected_server: "auto".
// It is tagged `proxy`, exactly like a single-server outbound, so nothing else
// in the config has to change.
type urltestOutbound struct {
	Type      string   `json:"type"`
	Tag       string   `json:"tag"`
	Outbounds []string `json:"outbounds"`
	URL       string   `json:"url"`
	Interval  string   `json:"interval"`
	Tolerance int      `json:"tolerance"`
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

	// The TUN inbound coexists with the mixed one: a user with TUN on usually
	// still wants 127.0.0.1:7897 for tools that take an explicit proxy.
	if rc.Config.Engine.Tun.Enabled {
		cfg.Inbounds = append(cfg.Inbounds, buildTunInbound(rc.Config.Engine.Tun))
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
	switch {
	case rc.Config.Proxy.Mode == config.ModeDirect:
		// Direct mode does not even reference a proxy outbound, so an
		// unusable selected server cannot break "direct".
		cfg.Outbounds = []any{direct, block}
	case rc.Config.IsAutoServer():
		// Auto mode renders every server plus a URLTest group tagged `proxy`.
		outbounds, err := buildAutoOutbounds(rc)
		if err != nil {
			return nil, err
		}
		cfg.Outbounds = append(outbounds, direct, block)
		final = tagProxy
	default:
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
	//
	// Under a TUN, global mode does get the sniff/hijack-dns actions, because
	// without them DNS would leave the host as raw packets to port 53 and the
	// engine's proxied resolver would never be consulted. Actions route
	// nothing, so global stays global.
	tun := rc.Config.Engine.Tun.Enabled
	// auto_detect_interface is what stops the engine from routing its own
	// connection to the proxy server back into its own TUN. When
	// engine.bind_interface is set the outbound is already pinned, so the
	// global setting would be redundant.
	autoDetect := tun && rc.Config.Engine.BindInterface == ""

	// The rule sets the config will declare: the built-in policy's in rule
	// mode, plus any TUN route-exclusion sets. Building them (and extracting
	// the files) is a no-op when the list is empty, which keeps the modes that
	// reference none byte-identical to M2's output.
	declared := declaredRuleSets(rc.Config)
	var sets []singboxRuleSet
	if len(declared) > 0 {
		sets, err = buildRuleSets(rc.RunDir, declared)
		if err != nil {
			return nil, err
		}
	}

	switch rc.Config.Proxy.Mode {
	case config.ModeRule:
		dns, err := buildDNS(rules.RuleModeDNS(rc.UserRules))
		if err != nil {
			return nil, err
		}
		cfg.DNS = dns

		routeRules, err := buildRules(rc.UserRules)
		if err != nil {
			return nil, err
		}
		cfg.Route = singboxRoute{
			RuleSet:             sets,
			AutoDetectInterface: autoDetect,
			// Destination names are resolved with the domestic resolver: it is
			// the one that can answer for the names a direct outbound dials,
			// and a proxied outbound never resolves locally at all (it passes
			// the name to the server), so this cannot leak a proxied lookup.
			// Crucially it is *not* the FakeIP resolver: a direct dial must get
			// a real address, never a synthetic one.
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
		cfg.Route = singboxRoute{
			RuleSet:             sets,
			AutoDetectInterface: autoDetect,
			Rules:               globalRules(tun),
			Final:               final,
		}

	default: // config.ModeDirect
		// Direct mode must not proxy, so it gets no actions either: with
		// hijack-dns the engine would answer DNS itself instead of letting the
		// packets out.
		cfg.Route = singboxRoute{
			RuleSet:             sets,
			AutoDetectInterface: autoDetect,
			Rules:               []singboxRule{},
			Final:               final,
		}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("core: encode sing-box config: %w", err)
	}
	return append(data, '\n'), nil
}

// buildTunInbound renders the optional TUN inbound from vender's own
// engine.tun block. Every field is passed through with the same name sing-box
// uses, so the doc and the generated file read alike.
//
// Two choices are worth recording:
//
//   - `address` is normalized to config.DefaultTunAddress rather than left
//     empty: sing-box 1.14 refuses to start a tun inbound without an interface
//     address ("missing interface address"). The default is a private /30 that
//     does not collide with the 198.18.0.0/15 mihomo/clash uses for its TUN.
//   - `stack` is always emitted, even when it equals the engine default, so a
//     change of default in a future sing-box cannot silently change vender's
//     behaviour.
func buildTunInbound(t config.TunConfig) singboxInbound {
	return singboxInbound{
		Type:                   "tun",
		Tag:                    tagTun,
		InterfaceName:          t.InterfaceName,
		Address:                t.Address,
		MTU:                    t.MTU,
		Stack:                  t.Stack,
		AutoRoute:              t.AutoRouteEnabled(),
		StrictRoute:            t.StrictRoute,
		RouteExcludeAddress:    t.RouteExcludeAddress,
		RouteExcludeAddressSet: t.RouteExcludeAddressSet,
	}
}

// declaredRuleSets is the list of rule set tags the generated config declares
// in route.rule_set: the built-in policy's in rule mode, plus the TUN
// route-exclusion sets in any mode. Duplicates keep their first position, so
// the generated file is deterministic.
func declaredRuleSets(cfg config.Config) []string {
	if cfg.Proxy.Mode != config.ModeRule && !cfg.Engine.Tun.Enabled {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(tags ...string) {
		for _, tag := range tags {
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	if cfg.Proxy.Mode == config.ModeRule {
		add(rules.ReferencedRuleSets()...)
	}
	if cfg.Engine.Tun.Enabled {
		add(cfg.Engine.Tun.RouteExcludeAddressSet...)
	}
	return out
}

// validateTunRuleSets rejects a TUN exclusion that names a rule set vender does
// not embed. It lives here rather than in internal/config because only core
// knows the embedded sets.
func validateTunRuleSets(cfg config.Config) error {
	if !cfg.Engine.Tun.Enabled {
		return nil
	}
	for _, tag := range cfg.Engine.Tun.RouteExcludeAddressSet {
		if _, ok := rules.LookupRuleSet(tag); !ok {
			return fmt.Errorf(
				"core: engine.tun.route_exclude_address_set %q is not one of the rule sets vender embeds (see `vender rules list`)", tag)
		}
	}
	return nil
}

// buildOutbound renders the selected server as the proxy outbound.
func buildOutbound(rc RuntimeConfig) (any, error) {
	if rc.Server == nil {
		return nil, errors.New("core: proxy mode requires a selected server")
	}
	return buildOutboundFor(rc, rc.Server, tagProxy)
}

// buildAutoOutbounds renders every configured server as its own outbound and
// wraps them in a URLTest group tagged `proxy`. The group's own tag is the one
// the policy refers to, so switching a server out for the group changes only
// this function.
func buildAutoOutbounds(rc RuntimeConfig) ([]any, error) {
	out := make([]any, 0, len(rc.Servers)+1)
	members := make([]string, 0, len(rc.Servers))
	seen := make(map[string]bool, len(rc.Servers))
	for i := range rc.Servers {
		srv := &rc.Servers[i]
		tag := ServerOutboundTag(srv)
		if seen[tag] {
			return nil, fmt.Errorf("core: two servers map to the outbound tag %q", tag)
		}
		seen[tag] = true
		ob, err := buildOutboundFor(rc, srv, tag)
		if err != nil {
			return nil, err
		}
		out = append(out, ob)
		members = append(members, tag)
	}
	out = append(out, urltestOutbound{
		Type:      "urltest",
		Tag:       tagProxy,
		Outbounds: members,
		URL:       autoFailoverURL,
		Interval:  rc.Config.Engine.FailoverIntervalDuration().String(),
		Tolerance: rc.Config.Engine.FailoverTolerance(),
	})
	return out, nil
}

// ServerOutboundTag is the sing-box tag of one server's outbound in auto mode.
// The server's own name is used, because that is what the Clash API reports and
// what the TUI shows; reserved vender tags are prefixed so a server cannot
// shadow them.
func ServerOutboundTag(srv *config.Server) string {
	switch srv.Name {
	case tagProxy, tagDirect, tagBlock, tagMixed, tagTun:
		return "server-" + srv.Name
	}
	return srv.Name
}

// buildOutboundFor renders one server as an outbound with the given tag.
func buildOutboundFor(rc RuntimeConfig, srv *config.Server, tag string) (any, error) {
	iface := rc.Config.Engine.BindInterface

	switch srv.Type {
	case config.TypeVLESS:
		out := vlessOutbound{
			Type:          "vless",
			Tag:           tag,
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
			Tag:           tag,
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

	case config.TypeShadowsocks:
		return shadowsocksOutbound{
			Type:          "shadowsocks",
			Tag:           tag,
			Server:        srv.Address,
			ServerPort:    srv.Port,
			Method:        srv.Method,
			Password:      srv.Password,
			BindInterface: iface,
		}, nil

	case config.TypeVMess:
		return vmessOutbound{
			Type:          "vmess",
			Tag:           tag,
			Server:        srv.Address,
			ServerPort:    srv.Port,
			UUID:          srv.UUID,
			Security:      srv.Cipher,
			AlterID:       srv.AlterID,
			BindInterface: iface,
			TLS:           v2rayTLS(srv),
			Transport:     v2rayTransportFor(srv),
		}, nil

	case config.TypeTrojan:
		return trojanOutbound{
			Type:          "trojan",
			Tag:           tag,
			Server:        srv.Address,
			ServerPort:    srv.Port,
			Password:      srv.Password,
			BindInterface: iface,
			TLS:           v2rayTLS(srv),
			Transport:     v2rayTransportFor(srv),
		}, nil

	default:
		return nil, fmt.Errorf("core: unsupported server type %q", srv.Type)
	}
}

// v2rayTLS renders the TLS block shared by vmess and trojan, or nil when the
// transport is plaintext (a trojan server is never plaintext, because
// config.Server.Normalize forces security=tls for it).
func v2rayTLS(srv *config.Server) *tlsConfig {
	if srv.Security != config.SecurityTLS {
		return nil
	}
	return &tlsConfig{
		Enabled:    true,
		ServerName: srv.SNI,
		Insecure:   srv.Insecure,
		ALPN:       srv.ALPN,
		UTLS:       utls(srv.Fingerprint),
	}
}

// v2rayTransportFor renders the transport block shared by vmess and trojan, or
// nil for a bare TCP stream with no HTTP header.
func v2rayTransportFor(srv *config.Server) *v2rayTransport {
	switch {
	case srv.Network == config.NetworkWS:
		out := &v2rayTransport{Type: "ws", Path: srv.Path}
		if out.Path == "" {
			out.Path = "/"
		}
		if srv.Host != "" {
			out.Headers = map[string]string{"Host": srv.Host}
		}
		return out

	case srv.Network == config.NetworkGRPC:
		return &v2rayTransport{Type: "grpc", ServiceName: srv.Path}

	case srv.HeaderType == config.HeaderTypeHTTP:
		out := &v2rayTransport{Type: "http", Path: srv.Path}
		if out.Path == "" {
			out.Path = "/"
		}
		if srv.Host != "" {
			out.Host = []string{srv.Host}
		}
		return out

	default:
		return nil
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
		if s.Tag == "" {
			return nil, fmt.Errorf("core: DNS server %q is incomplete", s.Tag)
		}
		// A fakeip server has no upstream address (it answers locally);
		// every other transport needs one.
		if s.Type != "fakeip" && s.Server == "" {
			return nil, fmt.Errorf("core: DNS server %q has no address", s.Tag)
		}
		out.Servers = append(out.Servers, singboxDNSServer{
			Tag:        s.Tag,
			Type:       s.Type,
			Server:     s.Server,
			Detour:     s.Detour,
			Inet4Range: s.Inet4Range,
			Inet6Range: s.Inet6Range,
		})
	}
	for _, r := range policy.Rules {
		if len(r.RuleSets)+len(r.Domains)+len(r.DomainSuffixes)+len(r.QueryTypes) == 0 {
			return nil, fmt.Errorf("core: DNS rule %q matches nothing", r.Name)
		}
		out.Rules = append(out.Rules, singboxDNSRule{
			Action:       "route",
			RuleSet:      r.RuleSets,
			Domain:       r.Domains,
			DomainSuffix: r.DomainSuffixes,
			QueryType:    r.QueryTypes,
			Server:       r.Server,
		})
	}
	return out, nil
}

// buildRules converts the merged policy into sing-box route rules: first the
// actions (sniff, hijack-dns), then the destination rules in evaluation order
// (critical, user, built-in).
//
// DNS-only rules are still skipped: sing-box expresses DNS policy as the
// `dns` section plus the hijack-dns action, not as destination matches, so a
// DNSOnly rule here would silently misroute DNS. Nothing emits one today.
func buildRules(user *rules.UserRules) ([]singboxRule, error) {
	policy, err := rules.EffectiveRules(user)
	if err != nil {
		return nil, err
	}
	out := buildActions()
	for _, r := range policy {
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

// buildActions renders the sniff + hijack-dns pair in evaluation order.
// Sniffing comes first because a connection that arrives as a bare IP is just
// an IP until the engine looks at its first bytes; hijack-dns then hands a DNS
// connection to the engine's DNS module instead of routing it as plain traffic.
func buildActions() []singboxRule {
	out := make([]singboxRule, 0, len(rules.BuiltinActions()))
	for _, a := range rules.BuiltinActions() {
		rule := singboxRule{Action: string(a.Kind)}
		if a.Protocol != "" {
			rule.Protocol = []string{a.Protocol}
		}
		out = append(out, rule)
	}
	return out
}

// globalRules is what global mode emits: nothing at all through the mixed
// inbound (global means "everything through the proxy", and the built-in
// destination rules would quietly turn it into rule mode), but the two actions
// when a TUN is up.
//
// The actions are needed under a TUN and harmless without one: sniff makes the
// proxied outbound receive the real destination name, and hijack-dns makes DNS
// go through the engine's proxied resolver instead of leaving the host as raw
// packets to port 53 — which is the whole point of global mode. They select no
// outbound, so the mode's contract is untouched.
func globalRules(tun bool) []singboxRule {
	if !tun {
		return []singboxRule{}
	}
	return buildActions()
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
