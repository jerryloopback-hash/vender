package subscription

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ll-lite/vender/internal/config"
)

// Clash / Mihomo support.
//
// A Clash "proxy provider" file is a YAML document whose `proxies:` key is a
// list of outbound definitions. vender reads the node fields that map onto its
// own Server and ignores the rest (rules, groups, experimental settings).
// Unknown node types are reported as skipped rather than silently dropped.
//
// The mapping is deliberately small: name, type, server, port and the fields
// that change how the connection is made. Anything a Clash node can express
// that vender cannot (a SIP003 plugin, a kcp/quic transport, a WireGuard node)
// is skipped with a message naming the node type or field.

type clashDoc struct {
	// Proxies is the modern key; Proxy is the legacy capitalisation Clash
	// Classic used in some exports.
	Proxies []clashProxy `yaml:"proxies"`
	Proxy   []clashProxy `yaml:"Proxy"`
}

type clashProxy struct {
	Name           string   `yaml:"name"`
	Type           string   `yaml:"type"`
	Server         string   `yaml:"server"`
	Port           int      `yaml:"port"`
	UUID           string   `yaml:"uuid"`
	Password       string   `yaml:"password"`
	Cipher         string   `yaml:"cipher"`
	AlterID        int      `yaml:"alterId"`
	Flow           string   `yaml:"flow"`
	TLS            bool     `yaml:"tls"`
	SNI            string   `yaml:"sni"`
	ServerName     string   `yaml:"servername"`
	Network        string   `yaml:"network"`
	SkipCertVerify bool     `yaml:"skip-cert-verify"`
	Plugin         string   `yaml:"plugin"`
	Fingerprint    string   `yaml:"client-fingerprint"`
	Obfs           string   `yaml:"obfs"`
	ObfsPassword   string   `yaml:"obfs-password"`
	ALPN           []string `yaml:"alpn"`

	WSOpts struct {
		Path    string            `yaml:"path"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"ws-opts"`

	HTTPOpts struct {
		Method  string            `yaml:"method"`
		Path    []string          `yaml:"path"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"http-opts"`

	GrpcOpts struct {
		ServiceName string `yaml:"grpc-service-name"`
	} `yaml:"grpc-opts"`

	RealityOpts struct {
		PublicKey string `yaml:"public-key"`
		ShortID   string `yaml:"short-id"`
	} `yaml:"reality-opts"`
}

// ParseClash parses Clash / Mihomo YAML with a proxies list.
func ParseClash(data []byte) (Result, error) {
	var doc clashDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Result{}, fmt.Errorf("subscription: not Clash YAML: %w", err)
	}
	nodes := doc.Proxies
	if len(nodes) == 0 {
		nodes = doc.Proxy
	}
	var res Result
	seen := make(map[string]bool)
	for _, n := range nodes {
		srv, err := n.server()
		if err != nil {
			res.Skipped = append(res.Skipped, Skipped{Node: nodeLabel(n), Reason: err.Error()})
			continue
		}
		if verr := srv.Validate(); verr != nil {
			res.Skipped = append(res.Skipped, Skipped{Node: srv.Name, Reason: verr.Error()})
			continue
		}
		key := strings.ToLower(srv.Name)
		if seen[key] {
			res.Skipped = append(res.Skipped, Skipped{Node: srv.Name, Reason: "duplicate node name in the subscription"})
			continue
		}
		seen[key] = true
		res.Servers = append(res.Servers, *srv)
	}
	if len(res.Servers) == 0 && len(nodes) == 0 {
		return res, fmt.Errorf("subscription: no `proxies:` list in the Clash YAML")
	}
	return res, nil
}

// server converts one Clash proxy definition into a config.Server.
func (n clashProxy) server() (*config.Server, error) {
	base := config.Server{
		Name:     strings.TrimSpace(n.Name),
		Address:  strings.TrimSpace(n.Server),
		Port:     n.Port,
		SNI:      firstNonEmpty(n.ServerName, n.SNI),
		ALPN:     n.ALPN,
		Insecure: n.SkipCertVerify,
	}

	switch strings.ToLower(strings.TrimSpace(n.Type)) {
	case "ss":
		if strings.TrimSpace(n.Plugin) != "" {
			return nil, fmt.Errorf("ss: this node needs a SIP003 plugin (%q), which vender does not support yet", n.Plugin)
		}
		base.Type = config.TypeShadowsocks
		base.Method = n.Cipher
		base.Password = n.Password

	case "vmess":
		base.Type = config.TypeVMess
		base.UUID = n.UUID
		base.AlterID = n.AlterID
		base.Cipher = n.Cipher
		base.Security = config.SecurityNone
		if n.TLS {
			base.Security = config.SecurityTLS
		}
		applyClashTransport(&base, n)
		base.Fingerprint = n.Fingerprint

	case "trojan":
		base.Type = config.TypeTrojan
		base.Password = n.Password
		base.Security = config.SecurityTLS
		base.Fingerprint = n.Fingerprint
		applyClashTransport(&base, n)

	case "vless":
		base.Type = config.TypeVLESS
		base.UUID = n.UUID
		base.Flow = n.Flow
		base.Security = config.SecurityNone
		switch {
		case n.RealityOpts.PublicKey != "":
			base.Security = config.SecurityReality
			base.PublicKey = n.RealityOpts.PublicKey
			base.ShortID = n.RealityOpts.ShortID
		case n.TLS:
			base.Security = config.SecurityTLS
		}
		base.Fingerprint = n.Fingerprint
		applyClashTransport(&base, n)

	case "hysteria2", "hy2":
		base.Type = config.TypeHysteria2
		base.Password = n.Password
		base.Obfs = n.Obfs
		base.ObfsPassword = n.ObfsPassword

	case "ssr", "snell", "wireguard", "hysteria", "tuic", "http", "socks5", "anytls", "ssh", "":
		return nil, fmt.Errorf("%s: node type is not supported by vender", nodeTypeLabel(n.Type))

	default:
		return nil, fmt.Errorf("%s: node type is not supported by vender", nodeTypeLabel(n.Type))
	}

	if base.Address == "" {
		return nil, fmt.Errorf("%s: node has no server address", nodeTypeLabel(n.Type))
	}
	base.Normalize()
	return &base, nil
}

// applyClashTransport maps a Clash network + transport options onto vender's
// network/path/host fields.
func applyClashTransport(s *config.Server, n clashProxy) {
	switch strings.ToLower(strings.TrimSpace(n.Network)) {
	case "", "tcp":
		// An HTTP-framed TCP stream is expressed by http-opts.
		if n.HTTPOpts.Method != "" || len(n.HTTPOpts.Path) > 0 {
			s.Network = config.NetworkTCP
			s.HeaderType = config.HeaderTypeHTTP
			s.Path = firstString(n.HTTPOpts.Path)
			s.Host = n.HTTPOpts.Headers["Host"]
			return
		}
		s.Network = config.NetworkTCP
	case "ws":
		s.Network = config.NetworkWS
		s.Path = n.WSOpts.Path
		s.Host = headerValue(n.WSOpts.Headers, "Host")
	case "grpc":
		s.Network = config.NetworkGRPC
		s.Path = n.GrpcOpts.ServiceName
	default:
		// kcp/quic/httpupgrade and friends: keep the value so Validate reports
		// it by name rather than the node silently connecting over TCP.
		s.Network = strings.ToLower(strings.TrimSpace(n.Network))
	}
}

func nodeLabel(n clashProxy) string {
	if strings.TrimSpace(n.Name) != "" {
		return strings.TrimSpace(n.Name)
	}
	return nodeTypeLabel(n.Type)
}

func nodeTypeLabel(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "unnamed node"
	}
	return t
}

func headerValue(h map[string]string, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func firstString(vals []string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
