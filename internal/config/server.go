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
	TypeVLESS     ServerType = "vless"
	TypeHysteria2 ServerType = "hysteria2"
)

// ProxyModes-like helper: the accepted server types.
func ServerTypes() []ServerType { return []ServerType{TypeVLESS, TypeHysteria2} }

// Valid reports whether t is a supported outbound protocol.
func (t ServerType) Valid() bool {
	switch t {
	case TypeVLESS, TypeHysteria2:
		return true
	default:
		return false
	}
}

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

	// --- vless + reality ---
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

	// --- hysteria2 ---
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
	}
	return strings.Join(parts, " ")
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
	s.Password = strings.TrimSpace(s.Password)
	s.Obfs = strings.TrimSpace(s.Obfs)
	s.ObfsPassword = strings.TrimSpace(s.ObfsPassword)
	s.PinSHA256 = strings.TrimSpace(s.PinSHA256)

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
		add("type %q is not one of vless, hysteria2", s.Type)
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
	}

	if len(problems) > 0 {
		return &ValidationError{Subject: s.String(), Problems: problems}
	}
	return nil
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
