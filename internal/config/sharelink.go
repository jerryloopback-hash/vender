package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Share-link parsing and rendering.
//
// Supported forms:
//
//	vless://<uuid>@<host>:<port>?<params>#<name>
//	hysteria2://<password>@<host>:<port>/?<params>#<name>
//	hy2://<password>@<host>:<port>/?<params>#<name>
//
// Parsing is deliberately liberal: every documented parameter alias is
// accepted, unknown parameters are ignored (so links from any panel load), and
// the error messages say exactly which part of the link was unusable.
// Rendering (Server.ShareLink) emits one canonical spelling.

// ErrInvalidLink marks a link that is recognisably a proxy share link but
// cannot be converted into a Server.
var ErrInvalidLink = errors.New("invalid share link")

// ErrUnsupportedScheme marks a link whose scheme vender does not implement.
var ErrUnsupportedScheme = errors.New("unsupported share link scheme")

// SupportedSchemes lists the URI schemes ParseShareLink understands.
func SupportedSchemes() []string { return []string{"vless", "hysteria2", "hy2"} }

// ParseShareLink converts a vless:// or hysteria2:// (hy2://) share link into a
// Server. The result is normalized but not validated; call Validate (or let
// ServerSet.Add do it) to reject links that are missing protocol-critical
// fields such as a REALITY public key.
func ParseShareLink(raw string) (*Server, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: empty link", ErrInvalidLink)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLink, err)
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("%w: missing scheme (expected vless://, hysteria2:// or hy2://)", ErrInvalidLink)
	}
	switch strings.ToLower(u.Scheme) {
	case "vless":
		return parseVLESS(u)
	case "hysteria2", "hy2":
		return parseHysteria2(u)
	default:
		return nil, fmt.Errorf("%w: %q (supported: %s)", ErrUnsupportedScheme, u.Scheme, strings.Join(SupportedSchemes(), ", "))
	}
}

// parseVLESS handles vless://<uuid>@<host>:<port>?<params>#<name>.
func parseVLESS(u *url.URL) (*Server, error) {
	if u.User == nil || strings.TrimSpace(u.User.Username()) == "" {
		return nil, fmt.Errorf("%w: vless: missing uuid (expected vless://<uuid>@<host>:<port>)", ErrInvalidLink)
	}
	uuid, err := NormalizeUUID(u.User.Username())
	if err != nil {
		return nil, fmt.Errorf("%w: vless: %v", ErrInvalidLink, err)
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	p := newParams(u.Query())
	srv := &Server{
		Name:        linkName(u, host, port),
		Type:        TypeVLESS,
		Address:     host,
		Port:        port,
		UUID:        uuid,
		Flow:        p.get("flow"),
		Security:    p.get("security"),
		SNI:         p.get("sni", "serverName", "servername", "peer"),
		Fingerprint: p.get("fp", "fingerprint"),
		PublicKey:   p.get("pbk", "publicKey", "public-key"),
		ShortID:     p.get("sid", "shortId", "short-id"),
		SpiderX:     p.get("spx", "spiderX", "spider-x"),
		Network:     p.get("type", "network"),
		HeaderType:  p.get("headerType", "header-type"),
	}
	// `encryption` is accepted and ignored: for VLESS it is always "none".
	if enc := p.get("encryption"); enc != "" && !strings.EqualFold(enc, "none") {
		return nil, fmt.Errorf("%w: vless: unsupported encryption %q (only none is supported)", ErrInvalidLink, enc)
	}
	// `spx` defaults to "/" on the wire; keep the field empty in that case so
	// servers.yaml stays readable.
	if srv.SpiderX == "/" {
		srv.SpiderX = ""
	}
	if !strings.EqualFold(srv.Security, SecurityReality) {
		// REALITY parameters are meaningless without security=reality.
		srv.PublicKey = ""
		srv.ShortID = ""
		srv.SpiderX = ""
	}
	srv.Normalize()
	return srv, nil
}

// parseHysteria2 handles hysteria2://<password>@<host>:<port>/?<params>#<name>.
func parseHysteria2(u *url.URL) (*Server, error) {
	p := newParams(u.Query())

	// The userinfo is the password ("user:pass" is rejoined for the hysteria2
	// userpass auth mode). Fall back to a query parameter for the rare panels
	// that put it there instead.
	password := userinfoAuth(u)
	if password == "" {
		password = p.get("password", "auth")
	}
	if password == "" {
		return nil, fmt.Errorf("%w: hysteria2: missing password (expected hysteria2://<password>@<host>:<port>)", ErrInvalidLink)
	}

	host, port, err := hostPort(u)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}

	srv := &Server{
		Name:         linkName(u, host, port),
		Type:         TypeHysteria2,
		Address:      host,
		Port:         port,
		Password:     password,
		SNI:          p.get("sni", "peer", "serverName"),
		Obfs:         p.get("obfs"),
		ObfsPassword: p.get("obfs-password", "obfsPassword", "obfspassword", "obfs_password"),
		PinSHA256:    p.get("pinSHA256", "pin-sha256", "pinsha256", "pin_sha256"),
		ALPN:         splitList(p.all("alpn")),
	}
	if v := p.get("insecure", "allowInsecure", "allow_insecure"); v != "" {
		insecure, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("%w: hysteria2: insecure=%q: %v", ErrInvalidLink, v, err)
		}
		srv.Insecure = insecure
	}
	srv.Normalize()
	return srv, nil
}

// ShareLink renders the server back into a share link. The output is canonical
// (parameters sorted, secrets percent-encoded) and is intentionally the only
// place that emits credentials.
func (s *Server) ShareLink() string {
	if s == nil {
		return ""
	}
	u := &url.URL{Fragment: s.Name}
	q := url.Values{}

	switch s.Type {
	case TypeHysteria2:
		u.Scheme = "hysteria2"
		u.User = url.User(s.Password)
		u.Path = "/"
		if s.SNI != "" {
			q.Set("sni", s.SNI)
		}
		if s.Obfs != "" {
			q.Set("obfs", s.Obfs)
			q.Set("obfs-password", s.ObfsPassword)
		}
		if s.Insecure {
			q.Set("insecure", "1")
		}
		if s.PinSHA256 != "" {
			q.Set("pinSHA256", s.PinSHA256)
		}
		if len(s.ALPN) > 0 {
			q.Set("alpn", strings.Join(s.ALPN, ","))
		}
	case TypeVLESS:
		u.Scheme = "vless"
		u.User = url.User(s.UUID)
		q.Set("encryption", "none")
		if s.Flow != "" {
			q.Set("flow", s.Flow)
		}
		if s.Security != "" {
			q.Set("security", s.Security)
		}
		if s.SNI != "" {
			q.Set("sni", s.SNI)
		}
		if s.Fingerprint != "" {
			q.Set("fp", s.Fingerprint)
		}
		if s.PublicKey != "" {
			q.Set("pbk", s.PublicKey)
		}
		if s.ShortID != "" {
			q.Set("sid", s.ShortID)
		}
		if s.SpiderX != "" {
			q.Set("spx", s.SpiderX)
		}
		if s.Network != "" {
			q.Set("type", s.Network)
		}
		if s.HeaderType != "" {
			q.Set("headerType", s.HeaderType)
		}
	default:
		return ""
	}

	u.Host = net.JoinHostPort(s.Address, strconv.Itoa(s.Port))
	u.RawQuery = q.Encode()
	return u.String()
}

// URL returns the parsed representation of ShareLink.
func (s *Server) URL() (*url.URL, error) {
	link := s.ShareLink()
	if link == "" {
		return nil, fmt.Errorf("no share link available for server type %q", s.Type)
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("re-parse generated link: %w", err)
	}
	return u, nil
}

// --- helpers ---------------------------------------------------------------

// params is a case-insensitive view of a link's query string that keeps every
// repeated value (needed for alpn=h2&alpn=http/1.1).
type params map[string][]string

func newParams(q url.Values) params {
	p := make(params, len(q))
	for k, vs := range q {
		key := strings.ToLower(strings.TrimSpace(k))
		for _, v := range vs {
			p[key] = append(p[key], v)
		}
	}
	return p
}

// get returns the first non-empty value among the aliases, or "".
func (p params) get(names ...string) string {
	for _, name := range names {
		for _, v := range p[strings.ToLower(name)] {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

// all returns every value stored under any of the aliases.
func (p params) all(names ...string) []string {
	var out []string
	for _, name := range names {
		out = append(out, p[strings.ToLower(name)]...)
	}
	return out
}

// splitList flattens values that may be comma-separated (alpn=h2,http/1.1) and
// drops empties, preserving order and removing duplicates.
func splitList(values []string) []string {
	var out []string
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// hostPort extracts and validates the host and port from a link.
func hostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("%w: missing host (expected <host>:<port> in %q)", ErrInvalidLink, u.Host)
	}
	portStr := u.Port()
	if portStr == "" {
		return "", 0, fmt.Errorf("%w: missing port (expected <host>:<port>, got %q)", ErrInvalidLink, u.Host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("%w: invalid port %q", ErrInvalidLink, portStr)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("%w: port %d out of range 1-65535", ErrInvalidLink, port)
	}
	return host, port, nil
}

// linkName derives the server name from the URL fragment, falling back to
// host:port (bracketed for IPv6) when the link carries no name.
func linkName(u *url.URL, host string, port int) string {
	name := strings.TrimSpace(u.Fragment) // url.Parse already percent-decoded it
	if name == "" {
		name = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return name
}

// userinfoAuth reconstructs the hysteria2 auth string from the URL userinfo.
func userinfoAuth(u *url.URL) string {
	if u.User == nil {
		return ""
	}
	user := u.User.Username()
	password, hasPassword := u.User.Password()
	switch {
	case !hasPassword:
		return user
	case user == "":
		return password
	default:
		return user + ":" + password
	}
}

// parseBool accepts the truthy/falsy spellings panels use in query strings.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("not a boolean (use 1/0 or true/false)")
	}
}
