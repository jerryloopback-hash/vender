package config

import (
	"encoding/base64"
	"encoding/json"
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
//	ss://<base64(method:password)>@<host>:<port>/?<params>#<name>   (SIP002)
//	ss://<base64(method:password@host:port)>#<name>                 (legacy)
//	vmess://<base64(json)>                                          (v2rayN JSON)
//	trojan://<password>@<host>:<port>?<params>#<name>
//
// Parsing is deliberately liberal: every documented parameter alias is
// accepted, unknown parameters are ignored (so links from any panel load), and
// the error messages say exactly which part of the link was unusable.
// Rendering (Server.ShareLink) emits one canonical spelling.
//
// The Shadowsocks and VMess forms are not parsed with net/url: their payload
// is a base64 blob that may legitimately contain `/` and `+`, which net/url
// would read as a path or a space. Both are scanned by hand.

// ErrInvalidLink marks a link that is recognisably a proxy share link but
// cannot be converted into a Server.
var ErrInvalidLink = errors.New("invalid share link")

// ErrUnsupportedScheme marks a link whose scheme vender does not implement.
var ErrUnsupportedScheme = errors.New("unsupported share link scheme")

// SupportedSchemes lists the URI schemes ParseShareLink understands.
func SupportedSchemes() []string {
	return []string{"vless", "hysteria2", "hy2", "ss", "vmess", "trojan"}
}

// ParseShareLink converts a supported share link into a Server. The result is
// normalized but not validated; call Validate (or let ServerSet.Add do it) to
// reject links that are missing protocol-critical fields such as a REALITY
// public key.
func ParseShareLink(raw string) (*Server, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: empty link", ErrInvalidLink)
	}
	// Shadowsocks and VMess carry a base64 payload that net/url would mangle.
	switch strings.ToLower(linkScheme(raw)) {
	case "ss":
		return parseShadowsocks(raw)
	case "vmess":
		return parseVMess(raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLink, err)
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("%w: missing scheme (expected vless://, hysteria2://, ss://, vmess:// or trojan://)", ErrInvalidLink)
	}
	switch strings.ToLower(u.Scheme) {
	case "vless":
		return parseVLESS(u)
	case "hysteria2", "hy2":
		return parseHysteria2(u)
	case "trojan":
		return parseTrojan(u)
	default:
		return nil, fmt.Errorf("%w: %q (supported: %s)", ErrUnsupportedScheme, u.Scheme, strings.Join(SupportedSchemes(), ", "))
	}
}

// linkScheme returns the lowercased scheme of raw ("vless" for
// "vless://..."), or "" when raw has no scheme separator.
func linkScheme(raw string) string {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(raw[:i]))
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

// parseShadowsocks handles both Shadowsocks link shapes:
//
//	SIP002   ss://<base64(method:password)>@<host>:<port>/?plugin=...#name
//	legacy   ss://<base64(method:password@host:port)>#name
//
// SIP003 plugins are deliberately not implemented in this milestone: a link
// that needs one is rejected with a clear message rather than silently
// connecting without the obfuscation the server expects.
func parseShadowsocks(raw string) (*Server, error) {
	rest := raw[strings.Index(raw, "://")+3:]

	// The fragment is the name; it is percent-decoded (SIP002 says so).
	name := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		name = percentDecode(rest[i+1:])
		rest = rest[:i]
	}
	// The query carries the (unsupported) plugin parameters.
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		query = rest[i+1:]
		rest = rest[:i]
	}

	var userinfo, hostport string
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		// SIP002: the authority may be followed by a bare "/".
		userinfo = rest[:i]
		hostport = strings.TrimSuffix(rest[i+1:], "/")
	} else {
		// Legacy: the whole authority is base64(method:password@host:port).
		decoded, err := base64DecodeLoose(rest)
		if err != nil {
			return nil, fmt.Errorf("%w: ss: %q is neither SIP002 (base64(method:password)@host:port) nor a base64 method:password@host:port: %v", ErrInvalidLink, rest, err)
		}
		j := strings.LastIndex(decoded, "@")
		if j < 0 {
			return nil, fmt.Errorf("%w: ss: decoded link %q has no host (want method:password@host:port)", ErrInvalidLink, decoded)
		}
		userinfo, hostport = decoded[:j], decoded[j+1:]
	}

	if p := newParams(parseQuery(query)); p.get("plugin") != "" {
		return nil, fmt.Errorf("%w: ss: this server needs a SIP003 plugin (%q), which vender does not support yet; ask your provider for a plain Shadowsocks node", ErrInvalidLink, p.get("plugin"))
	}

	method, password, err := splitSSUserinfo(userinfo)
	if err != nil {
		return nil, err
	}
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	srv := &Server{
		Name:     name,
		Type:     TypeShadowsocks,
		Address:  host,
		Port:     port,
		Method:   method,
		Password: password,
	}
	srv.Normalize()
	return srv, nil
}

// splitSSUserinfo extracts method and password from the SIP002 userinfo, which
// may be base64(method:password) or a percent-encoded method:password.
func splitSSUserinfo(userinfo string) (method, password string, err error) {
	candidate := userinfo
	if decoded, derr := base64DecodeLoose(userinfo); derr == nil && strings.Contains(decoded, ":") {
		candidate = decoded
	} else {
		candidate = percentDecode(userinfo)
	}
	i := strings.Index(candidate, ":")
	if i <= 0 {
		return "", "", fmt.Errorf("%w: ss: userinfo %q is not method:password", ErrInvalidLink, userinfo)
	}
	method = strings.TrimSpace(candidate[:i])
	password = candidate[i+1:]
	if method == "" {
		return "", "", fmt.Errorf("%w: ss: method is empty", ErrInvalidLink)
	}
	if password == "" {
		return "", "", fmt.Errorf("%w: ss: password is empty", ErrInvalidLink)
	}
	return method, password, nil
}

// parseVMess handles vmess://<base64(json)> (the v2rayN JSON form). TCP, WS and
// gRPC transports with optional TLS are supported; anything else is rejected
// with the offending field named.
func parseVMess(raw string) (*Server, error) {
	rest := raw[strings.Index(raw, "://")+3:]

	// Some links append a #name; the canonical form carries the name in "ps".
	fragment := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		fragment = percentDecode(rest[i+1:])
		rest = rest[:i]
	}
	decoded, err := base64DecodeLoose(strings.TrimSpace(rest))
	if err != nil {
		return nil, fmt.Errorf("%w: vmess: payload is not base64: %v", ErrInvalidLink, err)
	}
	var v vmessJSON
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return nil, fmt.Errorf("%w: vmess: payload is not the v2rayN JSON object: %v", ErrInvalidLink, err)
	}

	host := strings.TrimSpace(v.Add)
	if host == "" {
		return nil, fmt.Errorf("%w: vmess: missing \"add\" (the server host)", ErrInvalidLink)
	}
	port, ok := rawInt(v.Port)
	if !ok || port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: vmess: invalid port %q", ErrInvalidLink, string(v.Port))
	}
	uuid, err := NormalizeUUID(v.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: vmess: %v", ErrInvalidLink, err)
	}
	aid := 0
	if n, ok := rawInt(v.Aid); ok {
		aid = n
	}

	network := strings.ToLower(strings.TrimSpace(v.Net))
	headerType := strings.ToLower(strings.TrimSpace(v.Type))
	switch network {
	case "", "tcp":
		network = NetworkTCP
	case "ws":
	case "grpc":
	case "http", "h2":
		network = NetworkTCP
		if headerType == "" || headerType == HeaderTypeNone {
			headerType = HeaderTypeHTTP
		}
	default:
		return nil, fmt.Errorf("%w: vmess: network %q is not supported (use tcp, ws or grpc)", ErrInvalidLink, network)
	}
	switch headerType {
	case "", HeaderTypeNone, HeaderTypeHTTP:
	default:
		return nil, fmt.Errorf("%w: vmess: header type %q is not supported (use none or http)", ErrInvalidLink, headerType)
	}

	security := SecurityNone
	if rawTruthy(v.TLS) {
		security = SecurityTLS
	}
	path := v.Path
	// For gRPC, the service name travels in "path" in the v2rayN JSON.
	name := strings.TrimSpace(v.PS)
	if name == "" {
		name = fragment
	}

	srv := &Server{
		Name:       name,
		Type:       TypeVMess,
		Address:    host,
		Port:       port,
		UUID:       uuid,
		AlterID:    aid,
		Cipher:     strings.TrimSpace(v.Scy),
		Network:    network,
		HeaderType: headerType,
		Security:   security,
		SNI:        strings.TrimSpace(v.SNI),
		Host:       strings.TrimSpace(v.Host),
		Path:       strings.TrimSpace(path),
		ALPN:       rawStringList(v.ALPN),
	}
	srv.Normalize()
	return srv, nil
}

// parseTrojan handles trojan://<password>@<host>:<port>?<params>#<name>.
func parseTrojan(u *url.URL) (*Server, error) {
	password := userinfoAuth(u)
	if password == "" {
		return nil, fmt.Errorf("%w: trojan: missing password (expected trojan://<password>@<host>:<port>)", ErrInvalidLink)
	}
	host, port, err := hostPort(u)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}
	p := newParams(u.Query())

	srv := &Server{
		Name:        linkName(u, host, port),
		Type:        TypeTrojan,
		Address:     host,
		Port:        port,
		Password:    password,
		Security:    SecurityTLS,
		SNI:         p.get("sni", "peer", "serverName"),
		Network:     p.get("type", "network"),
		Path:        p.get("path", "serviceName", "service_name"),
		Host:        p.get("host"),
		Fingerprint: p.get("fp", "fingerprint"),
		ALPN:        splitList(p.all("alpn")),
	}
	if v := p.get("allowInsecure", "insecure", "skip-cert-verify", "skipCertVerify"); v != "" {
		insecure, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("%w: trojan: insecure=%q: %v", ErrInvalidLink, v, err)
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
	case TypeShadowsocks:
		// SIP002: the credentials are a URL-safe base64 of method:password, so
		// the link never needs percent-encoding.
		u.Scheme = "ss"
		u.User = url.User(base64.RawURLEncoding.EncodeToString([]byte(s.Method + ":" + s.Password)))
		u.Host = net.JoinHostPort(s.Address, strconv.Itoa(s.Port))
		return u.String()
	case TypeTrojan:
		u.Scheme = "trojan"
		u.User = url.User(s.Password)
		if s.SNI != "" {
			q.Set("sni", s.SNI)
		}
		if s.Fingerprint != "" {
			q.Set("fp", s.Fingerprint)
		}
		switch {
		case s.Network == NetworkWS:
			q.Set("type", NetworkWS)
		case s.Network == NetworkGRPC:
			q.Set("type", NetworkGRPC)
		case s.HeaderType == HeaderTypeHTTP:
			q.Set("type", NetworkHTTP)
		}
		if s.Path != "" {
			q.Set("path", s.Path)
		}
		if s.Host != "" {
			q.Set("host", s.Host)
		}
		if s.Insecure {
			q.Set("allowInsecure", "1")
		}
		if len(s.ALPN) > 0 {
			q.Set("alpn", strings.Join(s.ALPN, ","))
		}
	case TypeVMess:
		// The v2rayN JSON form: a base64 object, with the name in "ps".
		return vmessShareLink(s)
	default:
		return ""
	}

	u.Host = net.JoinHostPort(s.Address, strconv.Itoa(s.Port))
	u.RawQuery = q.Encode()
	return u.String()
}

// vmessShareLink renders the v2rayN vmess:// form.
func vmessShareLink(s *Server) string {
	transport := s.Network
	headerType := s.HeaderType
	if transport == "" {
		transport = NetworkTCP
	}
	if transport == NetworkTCP && headerType != HeaderTypeHTTP {
		headerType = ""
	}
	tls := ""
	if s.Security == SecurityTLS {
		tls = "tls"
	}
	obj := vmessEmit{
		V:    "2",
		PS:   s.Name,
		Add:  s.Address,
		Port: s.Port,
		ID:   s.UUID,
		Aid:  s.AlterID,
		Scy:  s.Cipher,
		Net:  transport,
		Type: headerType,
		Host: s.Host,
		Path: s.Path,
		TLS:  tls,
		SNI:  s.SNI,
		ALPN: s.ALPN,
	}
	data, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(data)
}

// vmessEmit is the JSON object a vmess:// link carries.
type vmessEmit struct {
	V    string   `json:"v"`
	PS   string   `json:"ps"`
	Add  string   `json:"add"`
	Port int      `json:"port"`
	ID   string   `json:"id"`
	Aid  int      `json:"aid"`
	Scy  string   `json:"scy"`
	Net  string   `json:"net"`
	Type string   `json:"type"`
	Host string   `json:"host,omitempty"`
	Path string   `json:"path,omitempty"`
	TLS  string   `json:"tls"`
	SNI  string   `json:"sni,omitempty"`
	ALPN []string `json:"alpn,omitempty"`
}

// vmessJSON is the lenient *parse* form: v2rayN panels disagree on whether
// port/aid/tls/alpn are numbers, strings or arrays, so each is kept raw and
// coerced on demand.
type vmessJSON struct {
	V    json.RawMessage `json:"v"`
	PS   string          `json:"ps"`
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Scy  string          `json:"scy"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  json.RawMessage `json:"tls"`
	SNI  string          `json:"sni"`
	ALPN json.RawMessage `json:"alpn"`
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

// base64DecodeLoose decodes a base64 payload in any of the four common
// spellings: standard or URL-safe, padded or not. Panels are inconsistent, and
// the difference is never meaningful for a share-link payload.
func base64DecodeLoose(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty payload")
	}
	encs := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encs {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		} else {
			lastErr = err
		}
	}
	return "", lastErr
}

// percentDecode decodes a percent-encoded string, falling back to the input
// when it is not valid (a name is cosmetic, so a malformed escape must not
// fail the whole link).
func percentDecode(s string) string {
	if decoded, err := url.PathUnescape(s); err == nil {
		return decoded
	}
	return s
}

// parseQuery turns a raw query string into url.Values without needing a URL.
func parseQuery(raw string) url.Values {
	v, err := url.ParseQuery(raw)
	if err != nil {
		// Keep whatever parsed; a stray bad escape must not fail the link.
		return v
	}
	return v
}

// splitHostPort splits "host:port", accepting a bracketed IPv6 literal.
func splitHostPort(s string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		// Some legacy links carry an unbracketed IPv4 host:port, which
		// SplitHostPort already handles; anything else is genuinely ambiguous.
		return "", 0, fmt.Errorf("%w: %q is not a host:port endpoint", ErrInvalidLink, s)
	}
	if host == "" {
		return "", 0, fmt.Errorf("%w: missing host in %q", ErrInvalidLink, s)
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

// rawInt coerces a JSON value that may be a number or a quoted number.
func rawInt(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v, true
		}
	}
	return 0, false
}

// rawTruthy reports whether a JSON value means "on": true, "tls", "true", "1".
func rawTruthy(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "tls", "true", "1", "yes", "on":
			return true
		}
	}
	return false
}

// rawStringList coerces a JSON value that may be a string, a comma-separated
// string, or an array of strings.
func rawStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return splitList(list)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return splitList([]string{s})
	}
	return nil
}

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
