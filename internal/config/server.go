package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ServerType is the outbound protocol of a server entry.
type ServerType string

const (
	TypeVLESS       ServerType = "vless"
	TypeHysteria2   ServerType = "hysteria2"
	TypeShadowsocks ServerType = "shadowsocks"
	TypeVMess       ServerType = "vmess"
	TypeTrojan      ServerType = "trojan"
)

// ProxyModes-like helper: the accepted server types.
func ServerTypes() []ServerType {
	return []ServerType{TypeVLESS, TypeHysteria2, TypeShadowsocks, TypeVMess, TypeTrojan}
}

// Valid reports whether t is a supported outbound protocol.
func (t ServerType) Valid() bool {
	switch t {
	case TypeVLESS, TypeHysteria2, TypeShadowsocks, TypeVMess, TypeTrojan:
		return true
	default:
		return false
	}
}

// SupportedShadowsocksMethods lists the ciphers the pinned sing-box accepts.
// It is validated here so a typo in a subscription is reported with vender's
// own words instead of the engine's ("unknown method").
func SupportedShadowsocksMethods() []string {
	return []string{
		"aes-128-ctr", "aes-192-ctr", "aes-256-ctr",
		"rc4-md5",
		"aes-128-gcm", "aes-192-gcm", "aes-256-gcm",
		"chacha20-ietf", "chacha20-ietf-poly1305", "xchacha20-ietf-poly1305",
		"none",
		"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305",
	}
}

// IsShadowsocks2022 reports whether method is one of the Shadowsocks 2022
// ciphers (whose password is a fixed-length base64 key rather than a
// passphrase).
func IsShadowsocks2022(method string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-")
}

// validShadowsocksMethod reports whether method is one vender knows.
func validShadowsocksMethod(method string) bool {
	m := strings.ToLower(strings.TrimSpace(method))
	for _, ok := range SupportedShadowsocksMethods() {
		if m == ok {
			return true
		}
	}
	return false
}

// Transport values shared by vmess and trojan ("network" in a share link).
const (
	// NetworkTCP is a plain stream (an optional HTTP header may be layered on
	// for vmess, see HeaderTypeHTTP).
	NetworkTCP = "tcp"
	// NetworkWS is a WebSocket transport.
	NetworkWS = "ws"
	// NetworkGRPC is a gRPC transport.
	NetworkGRPC = "grpc"
	// NetworkHTTP is a vmess HTTP-header stream; it is normalised to TCP with
	// HeaderTypeHTTP so the stored form is unambiguous.
	NetworkHTTP = "http"
)

// Header types for a vmess TCP stream.
const (
	// HeaderTypeNone is a bare TCP stream (the default).
	HeaderTypeNone = "none"
	// HeaderTypeHTTP wraps the stream in an HTTP request header.
	HeaderTypeHTTP = "http"
)

// Security values for the VLESS transport.
const (
	// SecurityNone is plain TCP without TLS.
	SecurityNone = "none"
	// SecurityTLS is TLS with a SNI.
	SecurityTLS = "tls"
	// SecurityReality is REALITY (M0's primary transport).
	SecurityReality = "reality"
)

// Server is a single outbound server entry in servers.yaml. It holds secrets
// (uuid, password, public key), so servers.yaml is written with mode 0600 and
// Server.String never prints them.
//
// Fields are grouped per protocol; a VLESS entry leaves the hysteria2 fields
// empty and vice versa.
type Server struct {
	Name string     `yaml:"name"`
	Type ServerType `yaml:"type"`
	// Address is the host or IP of the server (YAML key "server").
	Address string `yaml:"server"`
	Port    int    `yaml:"port"`
	// Profile names the profile this server belongs to (profiles.yaml). A
	// missing value loads as ProfileManual, which is what keeps a
	// pre-M7 servers.yaml working unchanged. It is a vender concept, so it is
	// not part of a share link.
	Profile string `yaml:"profile,omitempty"`

	// --- vless + reality (and the TLS layer of vmess / trojan) ---
	UUID        string `yaml:"uuid,omitempty"`
	Flow        string `yaml:"flow,omitempty"`
	Security    string `yaml:"security,omitempty"`
	SNI         string `yaml:"sni,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty"`
	PublicKey   string `yaml:"public_key,omitempty"`
	ShortID     string `yaml:"short_id,omitempty"`
	SpiderX     string `yaml:"spider_x,omitempty"`
	Network     string `yaml:"network,omitempty"`
	HeaderType  string `yaml:"header_type,omitempty"`
	// Path and Host describe the vmess/trojan transport: the WebSocket path
	// (or the HTTP-header path), and the host header it is sent with. They are
	// unused by vless.
	Path string `yaml:"path,omitempty"`
	Host string `yaml:"host,omitempty"`

	// --- vmess ---
	// AlterID is the vmess "aid" (0 for AEAD-only servers).
	AlterID int `yaml:"alter_id,omitempty"`
	// Cipher is the vmess "scy" (security). "auto" is the common default.
	Cipher string `yaml:"cipher,omitempty"`

	// --- shadowsocks ---
	// Method is the Shadowsocks cipher ("aes-256-gcm", "2022-blake3-aes-256-gcm").
	Method string `yaml:"method,omitempty"`

	// --- hysteria2, and the shared secret of shadowsocks / trojan ---
	Password     string   `yaml:"password,omitempty"`
	Obfs         string   `yaml:"obfs,omitempty"`
	ObfsPassword string   `yaml:"obfs_password,omitempty"`
	Insecure     bool     `yaml:"insecure,omitempty"`
	PinSHA256    string   `yaml:"pin_sha256,omitempty"`
	ALPN         []string `yaml:"alpn,omitempty"`
}

// Endpoint renders "host:port", bracketing IPv6 literals.
func (s *Server) Endpoint() string {
	return net.JoinHostPort(s.Address, strconv.Itoa(s.Port))
}

// String is a redacted, human-readable summary. It never contains secrets, so
// it is safe to embed in errors, logs and table output.
func (s *Server) String() string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s %q (%s)", s.Type, s.Name, s.Endpoint())
}

// Detail is a short protocol-specific description for `servers list`.
func (s *Server) Detail() string {
	var parts []string
	switch s.Type {
	case TypeVLESS:
		if s.Security != "" {
			sec := s.Security
			if s.Flow != "" {
				sec += "/" + s.Flow
			}
			parts = append(parts, sec)
		}
		if s.Network != "" {
			parts = append(parts, s.Network)
		}
		if s.SNI != "" {
			parts = append(parts, "sni="+s.SNI)
		}
	case TypeHysteria2:
		if s.Obfs != "" {
			parts = append(parts, "obfs="+s.Obfs)
		}
		if s.SNI != "" {
			parts = append(parts, "sni="+s.SNI)
		}
		if s.Insecure {
			parts = append(parts, "insecure")
		}
	case TypeShadowsocks:
		if s.Method != "" {
			parts = append(parts, s.Method)
		}
	case TypeVMess:
		if s.Cipher != "" {
			parts = append(parts, "scy="+s.Cipher)
		}
		parts = appendTransportDetail(parts, s)
	case TypeTrojan:
		parts = appendTransportDetail(parts, s)
	}
	return strings.Join(parts, " ")
}

// appendTransportDetail adds the TLS/transport summary shared by vmess and
// trojan to a Detail() parts list.
func appendTransportDetail(parts []string, s *Server) []string {
	if s.Security == SecurityTLS {
		parts = append(parts, "tls")
	}
	if s.Network != "" {
		net := s.Network
		if s.Network == NetworkTCP && s.HeaderType == HeaderTypeHTTP {
			net = "http"
		}
		parts = append(parts, net)
	}
	if s.SNI != "" {
		parts = append(parts, "sni="+s.SNI)
	}
	if s.Insecure {
		parts = append(parts, "insecure")
	}
	return parts
}

// Normalize applies per-type defaults and canonicalises values. It is applied
// when a server is created from a share link or read from disk, and is
// idempotent.
//
// Deliberately NOT defaulted: flow. A REALITY link that omits `flow` means the
// server expects a plain TLS-in-TLS stream, so inventing xtls-rprx-vision here
// would silently break it.
func (s *Server) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	s.Type = ServerType(strings.ToLower(strings.TrimSpace(string(s.Type))))
	s.Address = strings.TrimSpace(s.Address)
	s.Profile = strings.TrimSpace(s.Profile)
	s.UUID = strings.ToLower(strings.TrimSpace(s.UUID))
	s.Flow = strings.TrimSpace(s.Flow)
	s.Security = strings.ToLower(strings.TrimSpace(s.Security))
	s.SNI = strings.TrimSpace(s.SNI)
	s.Fingerprint = strings.TrimSpace(s.Fingerprint)
	s.PublicKey = strings.TrimSpace(s.PublicKey)
	s.ShortID = strings.ToLower(strings.TrimSpace(s.ShortID))
	s.SpiderX = strings.TrimSpace(s.SpiderX)
	s.Network = strings.ToLower(strings.TrimSpace(s.Network))
	s.HeaderType = strings.ToLower(strings.TrimSpace(s.HeaderType))
	s.Path = strings.TrimSpace(s.Path)
	s.Host = strings.TrimSpace(s.Host)
	s.Cipher = strings.ToLower(strings.TrimSpace(s.Cipher))
	s.Method = strings.ToLower(strings.TrimSpace(s.Method))
	s.Password = strings.TrimSpace(s.Password)
	s.Obfs = strings.TrimSpace(s.Obfs)
	s.ObfsPassword = strings.TrimSpace(s.ObfsPassword)
	s.PinSHA256 = strings.TrimSpace(s.PinSHA256)

	// The manual profile is implicit: an explicit `profile: manual` is folded
	// back to absent, so a hand-edited file and a generated one agree, and a
	// pre-M7 servers.yaml round-trips byte for byte.
	if strings.EqualFold(s.Profile, ProfileManual) {
		s.Profile = ""
	}
	if s.Name == "" {
		s.Name = s.Endpoint()
	}

	switch s.Type {
	case TypeVLESS:
		if s.Network == "" {
			s.Network = "tcp"
		}
		if s.Security == SecurityReality || s.Security == SecurityTLS {
			if s.Fingerprint == "" {
				s.Fingerprint = "chrome"
			}
		}
		if s.HeaderType == "" {
			s.HeaderType = "none"
		}
	case TypeHysteria2:
		if strings.EqualFold(s.Obfs, "none") {
			s.Obfs = ""
		}
		// For hysteria2 keep the slice nil when empty so YAML `omitempty` behaves.
		var alpn []string
		for _, a := range s.ALPN {
			if a = strings.TrimSpace(a); a != "" {
				alpn = append(alpn, a)
			}
		}
		s.ALPN = alpn
	case TypeVMess:
		s.NormalizeV2RayTransport()
		if s.Cipher == "" {
			s.Cipher = "auto"
		}
	case TypeTrojan:
		// Trojan is TLS by definition; there is no plaintext variant.
		s.Security = SecurityTLS
		s.NormalizeV2RayTransport()
	case TypeShadowsocks:
		// The plugin field is intentionally unsupported: a link that needs one
		// is rejected at parse time rather than silently misbehaving.
	}
}

// NormalizeV2RayTransport canonicalises the network/header fields shared by
// vmess and trojan. `http` is folded into `tcp` + header_type `http` so the
// stored form has exactly one spelling; an empty network means plain TCP.
func (s *Server) NormalizeV2RayTransport() {
	if s.Network == "" {
		s.Network = NetworkTCP
	}
	if s.Network == NetworkHTTP || s.Network == "h2" {
		s.Network = NetworkTCP
		s.HeaderType = HeaderTypeHTTP
	}
	if s.HeaderType == "" {
		s.HeaderType = HeaderTypeNone
	}
	if s.HeaderType != HeaderTypeHTTP {
		s.HeaderType = HeaderTypeNone
	}
	if s.Security == "" {
		s.Security = SecurityNone
	}
	if s.Security == SecurityTLS && s.Fingerprint == "" {
		s.Fingerprint = "chrome"
	}
	// A path/host only means something for ws, grpc and the http header.
	switch {
	case s.Network == NetworkWS:
		if s.Path == "" {
			s.Path = "/"
		}
	case s.HeaderType == HeaderTypeHTTP:
		if s.Path == "" {
			s.Path = "/"
		}
	case s.Network != NetworkGRPC:
		s.Path = ""
		s.Host = ""
	}
}

// Validate checks required fields for the entry's type. It does not mutate the
// server; call Normalize first if the value came from user input.
func (s *Server) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if s.Name == "" {
		add("name must not be empty")
	} else if strings.ContainsAny(s.Name, "\n\r\t") {
		add("name must not contain control characters: %q", s.Name)
	}

	if !s.Type.Valid() {
		add("type %q is not one of vless, hysteria2, shadowsocks, vmess, trojan", s.Type)
	}
	if s.Address == "" {
		add("server must not be empty")
	} else if strings.ContainsAny(s.Address, " \t/") {
		add("server %q must be a hostname or IP address without spaces or slashes", s.Address)
	}
	if s.Port < 1 || s.Port > 65535 {
		add("port %d out of range 1-65535", s.Port)
	}

	switch s.Type {
	case TypeVLESS:
		if s.UUID == "" {
			add("uuid is required for vless")
		} else if _, err := NormalizeUUID(s.UUID); err != nil {
			add("uuid: %v", err)
		}
		switch s.Security {
		case "", SecurityNone:
			// plain TCP, nothing else required
		case SecurityTLS:
			if s.SNI == "" {
				add("sni is required for vless with security=tls")
			}
		case SecurityReality:
			if s.PublicKey == "" {
				add("public_key (pbk) is required for vless with security=reality")
			}
			if s.SNI == "" {
				add("sni is required for vless with security=reality")
			}
		default:
			add("security %q is not one of none, tls, reality", s.Security)
		}
		switch s.Network {
		case "", "tcp":
			// M0 supports TCP only; validated below so a ws/grpc link fails loudly
		default:
			add("network %q is not supported in this milestone (tcp only)", s.Network)
		}

	case TypeHysteria2:
		if s.Password == "" {
			add("password is required for hysteria2")
		}
		if s.Obfs != "" && s.ObfsPassword == "" {
			add("obfs_password is required when obfs is set (%q)", s.Obfs)
		}
		if s.Obfs == "" && s.ObfsPassword != "" {
			add("obfs_password is set but obfs is empty")
		}

	case TypeShadowsocks:
		if s.Method == "" {
			add("method is required for shadowsocks (the cipher, e.g. aes-256-gcm)")
		} else if !validShadowsocksMethod(s.Method) {
			add("method %q is not a Shadowsocks cipher sing-box supports (see `vender servers list`)", s.Method)
		}
		if s.Password == "" {
			add("password is required for shadowsocks")
		}

	case TypeVMess:
		if s.UUID == "" {
			add("uuid is required for vmess")
		} else if _, err := NormalizeUUID(s.UUID); err != nil {
			add("uuid: %v", err)
		}
		validateTLSForV2Ray(s.Type, s.Security, add)
		validateNetworkForV2Ray(s.Type, s.Network, s.HeaderType, add)

	case TypeTrojan:
		if s.Password == "" {
			add("password is required for trojan")
		}
		validateTLSForV2Ray(s.Type, s.Security, add)
		validateNetworkForV2Ray(s.Type, s.Network, s.HeaderType, add)
	}

	if len(problems) > 0 {
		return &ValidationError{Subject: s.String(), Problems: problems}
	}
	return nil
}

// validateTLSForV2Ray checks the TLS layer of a vmess or trojan entry. Both
// support plain TLS or none; REALITY is a VLESS-only transport and is rejected
// rather than silently rendered as plain TLS.
func validateTLSForV2Ray(t ServerType, security string, add func(string, ...any)) {
	switch security {
	case "", SecurityNone, SecurityTLS:
	default:
		add("security %q is not one of none, tls for %s", security, t)
	}
}

// validateNetworkForV2Ray checks the transport of a vmess or trojan entry
// against what sing-box supports for them.
func validateNetworkForV2Ray(t ServerType, network, headerType string, add func(string, ...any)) {
	switch network {
	case "", NetworkTCP, NetworkWS, NetworkGRPC:
	case NetworkHTTP, "h2":
		// Normalize folds these into TCP + header_type http; accepted here so a
		// not-yet-normalized value does not fail validation.
	default:
		add("network %q is not supported for %s (use tcp, ws or grpc)", network, t)
	}
	if headerType != "" && headerType != HeaderTypeNone && headerType != HeaderTypeHTTP {
		add("header_type %q is not supported (use none or http)", headerType)
	}
	if headerType == HeaderTypeHTTP && network != "" && network != NetworkTCP && network != NetworkHTTP && network != "h2" {
		add("header_type http applies to a tcp transport, not %q", network)
	}
}

// ValidationError aggregates every problem found, so a user fixing a
// hand-edited YAML sees all mistakes at once.
type ValidationError struct {
	Subject  string
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return fmt.Sprintf("invalid %s: %s", e.Subject, e.Problems[0])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "invalid %s:", e.Subject)
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  - %s", p)
	}
	return b.String()
}

// ErrNotFound is returned when a named server does not exist.
var ErrNotFound = errors.New("server not found")

// ErrDuplicate is returned when adding a name that already exists.
var ErrDuplicate = errors.New("server name already exists")

// ServerSet is the contents of servers.yaml.
type ServerSet struct {
	Servers []Server `yaml:"servers"`
}

// NewServerSet returns an empty set (never nil, so YAML emits `servers: []`).
func NewServerSet() *ServerSet {
	return &ServerSet{Servers: []Server{}}
}

// Get returns the server with the given name (case-insensitive) and whether it
// was found. The returned pointer aliases the set; mutate via the set's API.
func (ss *ServerSet) Get(name string) (*Server, bool) {
	for i := range ss.Servers {
		if strings.EqualFold(ss.Servers[i].Name, name) {
			return &ss.Servers[i], true
		}
	}
	return nil, false
}

// Names returns server names in order.
func (ss *ServerSet) Names() []string {
	names := make([]string, 0, len(ss.Servers))
	for i := range ss.Servers {
		names = append(names, ss.Servers[i].Name)
	}
	return names
}

// Len returns the number of servers.
func (ss *ServerSet) Len() int { return len(ss.Servers) }

// Add appends a validated server. It fails if the name is already taken unless
// overwrite is true, in which case the existing entry is replaced in place
// (preserving its position in the list).
func (ss *ServerSet) Add(srv Server, overwrite bool) error {
	srv.Normalize()
	if err := srv.Validate(); err != nil {
		return err
	}
	if existing, ok := ss.Get(srv.Name); ok {
		if !overwrite {
			return fmt.Errorf("%w: %q (use --force to replace it, or --name to import it under a different name)", ErrDuplicate, srv.Name)
		}
		*existing = srv
		return nil
	}
	ss.Servers = append(ss.Servers, srv)
	return nil
}

// Remove deletes the named server and reports whether it existed.
func (ss *ServerSet) Remove(name string) bool {
	for i := range ss.Servers {
		if strings.EqualFold(ss.Servers[i].Name, name) {
			ss.Servers = append(ss.Servers[:i], ss.Servers[i+1:]...)
			return true
		}
	}
	return false
}

// Validate checks every entry and rejects duplicate names.
func (ss *ServerSet) Validate() error {
	var problems []string
	seen := make(map[string]int, len(ss.Servers))
	for i := range ss.Servers {
		srv := &ss.Servers[i]
		label := srv.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if err := srv.Validate(); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		key := strings.ToLower(srv.Name)
		if prev, dup := seen[key]; dup {
			problems = append(problems, fmt.Sprintf("duplicate server name %q (entries %d and %d)", srv.Name, prev+1, i+1))
			continue
		}
		seen[key] = i
	}
	if len(problems) > 0 {
		return &ValidationError{Subject: "servers.yaml", Problems: problems}
	}
	return nil
}

// FindAll returns every server whose name contains sub (case-insensitive).
func (ss *ServerSet) FindAll(sub string) []*Server {
	var out []*Server
	for i := range ss.Servers {
		if strings.Contains(strings.ToLower(ss.Servers[i].Name), strings.ToLower(sub)) {
			out = append(out, &ss.Servers[i])
		}
	}
	return out
}

// NormalizeUUID canonicalises a UUID: it accepts the dashed form, 32 bare hex
// digits, and the Microsoft `{...}` braced form, and returns lowercase
// 8-4-4-4-12. VLESS requires a real UUID, so anything else is an error.
func NormalizeUUID(in string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return "", fmt.Errorf("%q is not a UUID (want 32 hex digits, optionally in 8-4-4-4-12 form)", in)
	}
	for _, r := range s {
		if !isHex(r) {
			return "", fmt.Errorf("%q is not a UUID (non-hex character %q)", in, r)
		}
	}
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32], nil
}

// IsValidUUID reports whether in is an acceptable UUID.
func IsValidUUID(in string) bool {
	_, err := NormalizeUUID(in)
	return err == nil
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

func parsePort(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
