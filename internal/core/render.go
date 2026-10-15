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
	Log       singboxLog       `json:"log"`
	Inbounds  []singboxInbound `json:"inbounds"`
	Outbounds []any            `json:"outbounds"`
	Route     singboxRoute     `json:"route"`
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
	Final string        `json:"final"`
	Rules []singboxRule `json:"rules"`
}

// singboxRule is the generated form of a rules.Rule. Only the fields sing-box
// shares with vender's model are emitted; a rule that matches nothing would
// never be produced (rules.Rule.Validate rejects it first).
type singboxRule struct {
	Domain        []string `json:"domain,omitempty"`
	DomainSuffix  []string `json:"domain_suffix,omitempty"`
	DomainKeyword []string `json:"domain_keyword,omitempty"`
	IPCIDR        []string `json:"ip_cidr,omitempty"`
	Port          []int    `json:"port,omitempty"`
	ProcessName   []string `json:"process_name,omitempty"`
	// Outbound is the tag the connection is routed to.
	Outbound string `json:"outbound"`
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

// BuildConfig renders rc into the sing-box configuration that M1 understands.
// It is pure: the result is a deterministic JSON document, which is what the
// golden tests compare against.
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

	// sing-box 1.13 removed the legacy inbound `sniff` option outright, so
	// vender never emits it; inbound sniffing is expressed as a route action
	// (`{"action":"sniff"}`) once M2 needs it.

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

	rules, err := buildRules()
	if err != nil {
		return nil, err
	}
	cfg.Route = singboxRoute{Final: final, Rules: rules}

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

// buildRules converts vender's built-in rule set into sing-box route rules.
//
// M1's rules.BuiltinRules() returns nil, meaning "no routing rules": every
// connection follows route.final. M2 fills in the rule sets and this function
// starts producing route rules without any change to the engine.
//
// DNS-only rules are skipped on purpose: sing-box expresses those as route
// *actions* (sniff / hijack-dns) plus `dns` rules, not as ordinary destination
// matches, so rendering them here would silently misroute DNS. M2 owns that
// mapping.
func buildRules() ([]singboxRule, error) {
	out := make([]singboxRule, 0)
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
			Domain:        r.Domains,
			DomainSuffix:  r.DomainSuffixes,
			DomainKeyword: r.DomainKeywords,
			IPCIDR:        r.IPCIDRs,
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
