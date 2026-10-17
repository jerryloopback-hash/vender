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
	// DNS is the provider's DNS policy. Only dns.nameserver-policy (the
	// explicit per-domain instruction) actually changes how vender resolves a
	// node hostname; dns.nameserver / dns.fallback are captured for auditing.
	DNS *clashDNS `yaml:"dns"`
}

// clashDNS is a provider's `dns:` block, decoded by hand so the
// nameserver-policy keeps its document order (a map would not) and so the raw
// block can be stored verbatim.
type clashDNS struct {
	// Policy is the nameserver-policy in document order.
	Policy []clashDNSPolicy
	// Nameservers and Fallback are the default resolver lists.
	Nameservers []string
	Fallback    []string
	// Raw is the `dns:` block as YAML, exactly as the provider wrote it.
	Raw string
}

type clashDNSPolicy struct {
	Domain  string
	Servers []string
}

// UnmarshalYAML decodes the dns mapping generously: keys are matched
// case-insensitively and accept both the dashed and underscored spelling, and
// every list is also accepted as a single scalar (panels disagree).
func (d *clashDNS) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Value == "" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	if raw, err := yaml.Marshal(node); err == nil {
		d.Raw = string(raw)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(node.Content[i].Value), "_", "-"))
		val := node.Content[i+1]
		switch key {
		case "nameserver-policy":
			if val.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(val.Content); j += 2 {
					d.Policy = append(d.Policy, clashDNSPolicy{
						Domain:  strings.TrimSpace(val.Content[j].Value),
						Servers: nodeStrings(val.Content[j+1]),
					})
				}
			}
		case "nameserver":
			d.Nameservers = nodeStrings(val)
		case "fallback":
			d.Fallback = nodeStrings(val)
		}
	}
	return nil
}

// nodeStrings reads a scalar or a sequence of scalars as a string list.
func nodeStrings(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		if s := strings.TrimSpace(node.Value); s != "" {
			return []string{s}
		}
	case yaml.SequenceNode:
		var out []string
		for _, n := range node.Content {
			if s := strings.TrimSpace(n.Value); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// profileDNS converts the decoded dns block into the policy vender stores.
// Only the nameserver-policy is applied; the default lists ride along as
// metadata. It returns nil when the provider declared nothing usable.
func (d *clashDNS) profileDNS() *config.ProfileDNS {
	if d == nil {
		return nil
	}
	out := &config.ProfileDNS{Nameservers: d.Nameservers, Fallback: d.Fallback, Raw: d.Raw}
	for _, p := range d.Policy {
		if len(p.Servers) == 0 {
			continue
		}
		// A pattern may list several servers (Clash tries them in order);
		// vender's outbound resolver is a single endpoint, so the first is
		// used and the rest stay visible in Raw.
		out.NamespacePolicy = append(out.NamespacePolicy, config.DNSNamespacePolicy{
			Domain: p.Domain,
			Server: p.Servers[0],
		})
	}
	out.Normalize()
	if out.IsEmpty() {
		return nil
	}
	return out
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
	res.DNS = doc.DNS.profileDNS()
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
