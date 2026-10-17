package config

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// All values below are fake. Never commit real credentials.
const (
	testUUID = "11111111-1111-1111-1111-111111111111"
	testPBK  = "DUMMY-FAKE-PUBLIC-KEY-FOR-TESTS-ONLY-0000000000"
	testSID  = "0123456789abcdef"
	testPW   = "22222222-3333-4444-5555-666666666666"
	testHost = "203.0.113.10"
)

// b64 is the standard-base64 encoding panels use for ss:// and vmess://
// payloads.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// ss2022TestKey is a valid 32-byte Shadowsocks-2022 key (base64 of 32 zero
// bytes), used for the cipher's fixed-length password.
const ss2022TestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestParseShareLinkVLESSReality(t *testing.T) {
	link := "vless://" + testUUID + "@" + testHost + ":443" +
		"?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.com" +
		"&fp=chrome&pbk=" + testPBK + "&sid=" + testSID +
		"&type=tcp&headerType=none#Test-VLESS-REALITY"

	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}

	want := Server{
		Name:        "Test-VLESS-REALITY",
		Type:        TypeVLESS,
		Address:     testHost,
		Port:        443,
		UUID:        testUUID,
		Flow:        "xtls-rprx-vision",
		Security:    SecurityReality,
		SNI:         "www.example.com",
		Fingerprint: "chrome",
		PublicKey:   testPBK,
		ShortID:     testSID,
		Network:     "tcp",
		HeaderType:  "none",
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("parsed server mismatch\n got: %#v\nwant: %#v", *got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestParseShareLinkVLESSAliases(t *testing.T) {
	// Every one of these parameters is an accepted alias of the canonical name
	// used in TestParseShareLinkVLESSReality. `encryption` is absent, which is
	// fine; `spx=/` is the on-the-wire default and must be stored as empty.
	link := "vless://" + strings.ToUpper(testUUID) + "@" + testHost + ":8443" +
		"?security=reality&serverName=alias.example.com&fingerprint=firefox" +
		"&publicKey=" + testPBK + "&shortId=" + testSID + "&spiderX=/" +
		"&network=tcp&headerType=none&unused=ignored#Alias-Node"

	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}

	if got.UUID != testUUID {
		t.Errorf("UUID = %q, want lowercased %q", got.UUID, testUUID)
	}
	if got.SNI != "alias.example.com" {
		t.Errorf("SNI = %q, want %q (serverName alias)", got.SNI, "alias.example.com")
	}
	if got.Fingerprint != "firefox" {
		t.Errorf("Fingerprint = %q, want %q (fingerprint alias)", got.Fingerprint, "firefox")
	}
	if got.PublicKey != testPBK {
		t.Errorf("PublicKey = %q (publicKey alias)", got.PublicKey)
	}
	if got.ShortID != testSID {
		t.Errorf("ShortID = %q (shortId alias)", got.ShortID)
	}
	if got.SpiderX != "" {
		t.Errorf("SpiderX = %q, want empty for the \"/\" default", got.SpiderX)
	}
	if got.Network != "tcp" {
		t.Errorf("Network = %q (network alias)", got.Network)
	}
	if got.Port != 8443 {
		t.Errorf("Port = %d, want 8443", got.Port)
	}
	// No encryption=... parameter at all: still accepted.
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestParseShareLinkVLESSTLSDefaults(t *testing.T) {
	link := "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=www.example.com&type=tcp#TLS-Node"
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	if got.Security != SecurityTLS {
		t.Errorf("Security = %q, want tls", got.Security)
	}
	if got.Fingerprint != "chrome" {
		t.Errorf("Fingerprint = %q, want the chrome default when fp is absent", got.Fingerprint)
	}
	if got.Network != "tcp" || got.HeaderType != "none" {
		t.Errorf("Network/HeaderType = %q/%q, want tcp/none defaults", got.Network, got.HeaderType)
	}
	if got.Flow != "" {
		t.Errorf("Flow = %q: flow must never be invented, an omitted flow means the server expects none", got.Flow)
	}
}

func TestParseShareLinkVLESSSecurityNotRealityDropsRealityFields(t *testing.T) {
	// A REALITY public key on a plain-TLS link is noise; it must not survive
	// into servers.yaml or the generated sing-box config.
	link := "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=www.example.com" +
		"&pbk=" + testPBK + "&sid=" + testSID + "&spx=%2Ftmp#TLS-Node"
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	if got.PublicKey != "" || got.ShortID != "" || got.SpiderX != "" {
		t.Errorf("reality fields kept on a tls link: pbk=%q sid=%q spx=%q", got.PublicKey, got.ShortID, got.SpiderX)
	}
}

func TestParseShareLinkHysteria2(t *testing.T) {
	link := "hysteria2://" + testPW + "@" + testHost + ":8443/" +
		"?sni=www.example.com&obfs=salamander&obfs-password=cafebabe&insecure=1#Test-Hy2"

	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}

	want := Server{
		Name:         "Test-Hy2",
		Type:         TypeHysteria2,
		Address:      testHost,
		Port:         8443,
		Password:     testPW,
		SNI:          "www.example.com",
		Obfs:         "salamander",
		ObfsPassword: "cafebabe",
		Insecure:     true,
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("parsed server mismatch\n got: %#v\nwant: %#v", *got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestParseShareLinkHysteria2Aliases(t *testing.T) {
	tests := []struct {
		name string
		link string
		want Server
	}{
		{
			name: "hy2 scheme, peer and obfsPassword aliases, insecure=true",
			link: "hy2://" + testPW + "@" + testHost + ":8443/?peer=www.example.com&obfs=salamander" +
				"&obfsPassword=cafebabe&insecure=true#Hy2-Alias",
			want: Server{
				Name: "Hy2-Alias", Type: TypeHysteria2, Address: testHost, Port: 8443,
				Password: testPW, SNI: "www.example.com", Obfs: "salamander",
				ObfsPassword: "cafebabe", Insecure: true,
			},
		},
		{
			name: "pinSHA256 and repeated alpn values",
			link: "hysteria2://" + testPW + "@" + testHost + ":8443/?sni=www.example.com" +
				"&pinSHA256=DUMMYPIN&alpn=h3&alpn=http%2F1.1#Hy2-Pin",
			want: Server{
				Name: "Hy2-Pin", Type: TypeHysteria2, Address: testHost, Port: 8443,
				Password: testPW, SNI: "www.example.com",
				PinSHA256: "DUMMYPIN", ALPN: []string{"h3", "http/1.1"},
			},
		},
		{
			name: "comma separated alpn, obfs=none is dropped, insecure=0",
			link: "hysteria2://" + testPW + "@" + testHost + ":443/?sni=www.example.com" +
				"&alpn=h3%2Ch2&obfs=none&insecure=0#Hy2-List",
			want: Server{
				Name: "Hy2-List", Type: TypeHysteria2, Address: testHost, Port: 443,
				Password: testPW, SNI: "www.example.com", ALPN: []string{"h3", "h2"},
			},
		},
		{
			name: "user:pass userinfo is rejoined for hysteria2 userpass auth",
			link: "hysteria2://user:pass@host.example.org:8443/#Hy2-UserPass",
			want: Server{
				Name: "Hy2-UserPass", Type: TypeHysteria2, Address: "host.example.org",
				Port: 8443, Password: "user:pass",
			},
		},
		{
			name: "password in query when userinfo is empty",
			link: "hysteria2://@host.example.org:8443/?password=" + testPW + "&sni=www.example.com#Hy2-Query",
			want: Server{
				Name: "Hy2-Query", Type: TypeHysteria2, Address: "host.example.org",
				Port: 8443, Password: testPW, SNI: "www.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShareLink(tt.link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("parsed server mismatch\n got: %#v\nwant: %#v", *got, tt.want)
			}
		})
	}
}

func TestParseShareLinkNames(t *testing.T) {
	tests := []struct {
		name     string
		link     string
		wantName string
	}{
		{
			name:     "decoded fragment",
			link:     "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=a.example.com#My%20Node%2FLA",
			wantName: "My Node/LA",
		},
		{
			name:     "missing fragment falls back to host:port",
			link:     "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=a.example.com",
			wantName: testHost + ":443",
		},
		{
			name:     "ipv6 fallback is bracketed",
			link:     "vless://" + testUUID + "@[2001:db8::1]:443?security=tls&sni=a.example.com",
			wantName: "[2001:db8::1]:443",
		},
		{
			name:     "unicode fragment",
			link:     "hysteria2://" + testPW + "@" + testHost + ":8443/?sni=a.example.com#%E6%97%A5%E6%9C%AC-%E4%B8%9C%E4%BA%AC",
			wantName: "日本-东京",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShareLink(tt.link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestParseShareLinkIPv6Host(t *testing.T) {
	link := "vless://" + testUUID + "@[2001:db8::1]:443?security=reality&sni=www.example.com" +
		"&pbk=" + testPBK + "&sid=" + testSID + "&type=tcp#IPv6-Node"

	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	if got.Address != "2001:db8::1" {
		t.Errorf("Address = %q, want the bare IPv6 literal without brackets", got.Address)
	}
	if got.Endpoint() != "[2001:db8::1]:443" {
		t.Errorf("Endpoint() = %q, want [2001:db8::1]:443", got.Endpoint())
	}
	if got := got.ShareLink(); !strings.Contains(got, "@[2001:db8::1]:443?") {
		t.Errorf("ShareLink() = %q, want a bracketed IPv6 host", got)
	}
}

func TestParseShareLinkErrors(t *testing.T) {
	tests := []struct {
		name    string
		link    string
		wantErr error
		wantSub string
	}{
		{name: "empty", link: "   ", wantErr: ErrInvalidLink},
		{name: "not a url", link: "this is not a link", wantErr: ErrInvalidLink},
		{name: "unsupported wireguard", link: "wireguard://" + testUUID + "@" + testHost + ":443", wantErr: ErrUnsupportedScheme},
		{name: "ss without an endpoint", link: "ss://" + b64("aes-256-gcm:pass"), wantErr: ErrInvalidLink},
		{name: "ss with a bad userinfo", link: "ss://bm90LWEtY2lwaGVy@" + testHost + ":8388#x", wantErr: ErrInvalidLink, wantSub: "method:password"},
		{name: "ss with a plugin", link: "ss://" + b64("aes-256-gcm:pass") + "@" + testHost + ":8388/?plugin=obfs-local%3Bobfs%3Dhttp#x",
			wantErr: ErrInvalidLink, wantSub: "plugin"},
		{name: "vmess with a non-base64 payload", link: "vmess://@@@not-base64@@@", wantErr: ErrInvalidLink, wantSub: "base64"},
		{name: "vmess with no port", link: "vmess://" + b64(`{"v":"2","add":"`+testHost+`","id":"`+testUUID+`"}`),
			wantErr: ErrInvalidLink, wantSub: "invalid port"},
		{name: "vmess with an unsupported network", link: "vmess://" + b64(`{"v":"2","add":"`+testHost+`","port":443,"id":"`+testUUID+`","net":"kcp"}`),
			wantErr: ErrInvalidLink, wantSub: "not supported"},
		{name: "trojan without a password", link: "trojan://@" + testHost + ":443#x", wantErr: ErrInvalidLink, wantSub: "missing password"},
		{
			name: "vless without uuid", link: "vless://" + testHost + ":443?security=tls#x",
			wantErr: ErrInvalidLink, wantSub: "missing uuid",
		},
		{
			name: "vless with malformed uuid", link: "vless://not-a-uuid@" + testHost + ":443#x",
			wantErr: ErrInvalidLink, wantSub: "not a UUID",
		},
		{
			name: "vless without port", link: "vless://" + testUUID + "@" + testHost + "?security=tls#x",
			wantErr: ErrInvalidLink, wantSub: "missing port",
		},
		{
			name: "vless with port 0", link: "vless://" + testUUID + "@" + testHost + ":0#x",
			wantErr: ErrInvalidLink, wantSub: "out of range",
		},
		{
			name: "vless with port too large", link: "vless://" + testUUID + "@" + testHost + ":70000#x",
			wantErr: ErrInvalidLink, wantSub: "out of range",
		},
		{
			name: "vless with non numeric port", link: "vless://" + testUUID + "@" + testHost + ":abc#x",
			wantErr: ErrInvalidLink, wantSub: "invalid port",
		},
		{
			name: "vless with empty host", link: "vless://" + testUUID + "@:443#x",
			wantErr: ErrInvalidLink, wantSub: "missing host",
		},
		{
			name: "vless with space in host", link: "vless://" + testUUID + "@exa mple.com:443#x",
			wantErr: ErrInvalidLink,
		},
		{
			name: "vless with unsupported encryption", link: "vless://" + testUUID + "@" + testHost + ":443?encryption=aes-128-gcm#x",
			wantErr: ErrInvalidLink, wantSub: "unsupported encryption",
		},
		{
			name: "hysteria2 without password", link: "hysteria2://" + testHost + ":8443/",
			wantErr: ErrInvalidLink, wantSub: "missing password",
		},
		{
			name: "hysteria2 with bad insecure", link: "hysteria2://" + testPW + "@" + testHost + ":8443/?insecure=maybe#x",
			wantErr: ErrInvalidLink, wantSub: "insecure",
		},
		{
			name: "hysteria2 without port", link: "hysteria2://" + testPW + "@" + testHost + "#x",
			wantErr: ErrInvalidLink, wantSub: "missing port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShareLink(tt.link)
			if err == nil {
				t.Fatalf("ParseShareLink(%q) = %#v, want an error", tt.link, got)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want it to wrap %v", err, tt.wantErr)
			}
			if tt.wantSub != "" && !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestParseShareLinkRejectsUnsupportedTransport(t *testing.T) {
	// A vless ws/grpc link parses fine but vless is TCP-only in vender;
	// validation catches it so `vender import` fails loudly instead of writing
	// a server that silently misbehaves. (vmess and trojan do support ws.)
	link := "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=www.example.com&type=ws&path=%2Fws#WS-Node"
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	err = got.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want a tcp-only error for a ws link")
	}
	if !strings.Contains(err.Error(), "tcp only") {
		t.Errorf("error = %q, want it to mention tcp only", err)
	}
}

func TestParseShareLinkShadowsocksSIP002(t *testing.T) {
	// The form the user's provider serves: base64(method:password) userinfo.
	link := "ss://" + b64("aes-256-gcm:dummy-password") + "@" + testHost + ":8388#Provider-Node"
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	want := Server{
		Name: "Provider-Node", Type: TypeShadowsocks, Address: testHost, Port: 8388,
		Method: "aes-256-gcm", Password: "dummy-password",
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("parsed server mismatch\n got: %#v\nwant: %#v", *got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// A 2022 cipher, whose password is a base64 key rather than a passphrase.
	key := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	s2022 := "ss://" + b64("2022-blake3-aes-256-gcm:"+key) + "@" + testHost + ":8389#SS2022"
	got2022, err := ParseShareLink(s2022)
	if err != nil {
		t.Fatalf("ParseShareLink(2022): %v", err)
	}
	if got2022.Method != "2022-blake3-aes-256-gcm" || got2022.Password != key {
		t.Errorf("2022 parse = %#v", *got2022)
	}
	if !IsShadowsocks2022(got2022.Method) {
		t.Errorf("IsShadowsocks2022(%q) = false", got2022.Method)
	}

	// Plain (un-encoded) userinfo is accepted too, as some panels emit it.
	plain := "ss://aes-128-gcm:pass@" + testHost + ":8388#Plain"
	if p, err := ParseShareLink(plain); err != nil || p.Method != "aes-128-gcm" || p.Password != "pass" {
		t.Errorf("plain userinfo parse = %#v, %v", p, err)
	}
}

func TestParseShareLinkShadowsocksLegacy(t *testing.T) {
	// The fully-base64 legacy form: no userinfo, no port separator to split on.
	link := "ss://" + b64("chacha20-ietf-poly1305:legacy-pass@"+testHost+":8388") + "#Legacy-Node"
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	want := Server{
		Name: "Legacy-Node", Type: TypeShadowsocks, Address: testHost, Port: 8388,
		Method: "chacha20-ietf-poly1305", Password: "legacy-pass",
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("legacy parse mismatch\n got: %#v\nwant: %#v", *got, want)
	}
}

func TestParseShareLinkVMess(t *testing.T) {
	// A realistic v2rayN payload: the port and aid are strings, the alpn is a
	// string, and the transport is WebSocket over TLS.
	payload := `{"v":"2","ps":"RealWorld-WS","add":"` + testHost + `","port":"443",` +
		`"id":"` + testUUID + `","aid":"0","scy":"auto","net":"ws","type":"none",` +
		`"host":"cdn.example.com","path":"/ray","tls":"tls","sni":"cdn.example.com","alpn":"h2,http/1.1"}`
	link := "vmess://" + b64(payload)

	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	want := Server{
		Name: "RealWorld-WS", Type: TypeVMess, Address: testHost, Port: 443,
		UUID: testUUID, Cipher: "auto", Security: SecurityTLS, SNI: "cdn.example.com",
		Fingerprint: "chrome", Network: NetworkWS, HeaderType: HeaderTypeNone,
		Path: "/ray", Host: "cdn.example.com", ALPN: []string{"h2", "http/1.1"},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("vmess parse mismatch\n got: %#v\nwant: %#v", *got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	t.Run("grpc service name travels in path", func(t *testing.T) {
		p := `{"v":"2","ps":"G","add":"` + testHost + `","port":443,"id":"` + testUUID + `","net":"grpc","path":"my-svc","tls":true}`
		g, err := ParseShareLink("vmess://" + b64(p))
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		if g.Network != NetworkGRPC || g.Path != "my-svc" || g.Security != SecurityTLS {
			t.Errorf("grpc parse = %#v", *g)
		}
	})

	t.Run("http network becomes tcp with an http header", func(t *testing.T) {
		p := `{"v":"2","ps":"H","add":"` + testHost + `","port":80,"id":"` + testUUID + `","net":"http","path":"/x","host":"h.example.com"}`
		g, err := ParseShareLink("vmess://" + b64(p))
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		if g.Network != NetworkTCP || g.HeaderType != HeaderTypeHTTP || g.Path != "/x" {
			t.Errorf("http parse = %#v", *g)
		}
		if g.Security != SecurityNone {
			t.Errorf("Security = %q, want none without tls", g.Security)
		}
	})

	t.Run("the name falls back to the fragment", func(t *testing.T) {
		p := `{"v":"2","add":"` + testHost + `","port":443,"id":"` + testUUID + `"}`
		g, err := ParseShareLink("vmess://" + b64(p) + "#From-Fragment")
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		if g.Name != "From-Fragment" {
			t.Errorf("Name = %q", g.Name)
		}
	})
}

func TestParseShareLinkTrojan(t *testing.T) {
	link := "trojan://dummy-password@" + testHost + ":443" +
		"?sni=www.example.com&type=ws&path=%2Ftrojan&host=www.example.com" +
		"&allowInsecure=1&alpn=h2&fp=chrome#Trojan-WS"
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	want := Server{
		Name: "Trojan-WS", Type: TypeTrojan, Address: testHost, Port: 443,
		Password: "dummy-password", Security: SecurityTLS, SNI: "www.example.com",
		Fingerprint: "chrome", Insecure: true, Network: NetworkWS,
		HeaderType: HeaderTypeNone, Path: "/trojan", Host: "www.example.com",
		ALPN: []string{"h2"},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("trojan parse mismatch\n got: %#v\nwant: %#v", *got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	t.Run("a bare trojan link is still TLS over TCP", func(t *testing.T) {
		g, err := ParseShareLink("trojan://pw@" + testHost + ":8443#T")
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		if g.Security != SecurityTLS || g.Network != NetworkTCP || g.HeaderType != HeaderTypeNone {
			t.Errorf("bare trojan = %#v", *g)
		}
	})
}

func TestShareLinkRoundTrip(t *testing.T) {
	links := []string{
		"vless://" + testUUID + "@" + testHost + ":443?encryption=none&flow=xtls-rprx-vision" +
			"&security=reality&sni=www.example.com&fp=chrome&pbk=" + testPBK +
			"&sid=" + testSID + "&type=tcp&headerType=none#Round-Trip-VLESS",
		"vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=www.example.com&spx=abc#RT-TLS",
		"vless://" + testUUID + "@[2001:db8::1]:443?security=reality&sni=www.example.com" +
			"&pbk=" + testPBK + "&sid=" + testSID + "#RT-IPv6",
		"hysteria2://" + testPW + "@" + testHost + ":8443/?sni=www.example.com&obfs=salamander" +
			"&obfs-password=cafebabe&insecure=1#RT-Hy2",
		"hy2://" + testPW + "@" + testHost + ":443/?sni=www.example.com&pinSHA256=DUMMYPIN&alpn=h3#RT-Hy2-Alias",
		"hysteria2://" + testPW + "@host.example.org:8443/#RT-NoParams",
		// M7: the new protocols, including both Shadowsocks spellings.
		"ss://" + b64("aes-256-gcm:p@ss-word") + "@" + testHost + ":8388#RT-SS-SIP002",
		"ss://" + b64("2022-blake3-aes-256-gcm:"+ss2022TestKey+"@"+testHost+":8389") + "#RT-SS-Legacy",
		"vmess://" + b64(`{"v":"2","ps":"RT-VMess","add":"`+testHost+`","port":"443","id":"`+testUUID+
			`","aid":"0","scy":"auto","net":"ws","type":"none","host":"cdn.example.com","path":"/rt","tls":"tls","sni":"cdn.example.com","alpn":["h2"]}`),
		"vmess://" + b64(`{"v":"2","ps":"RT-VMess-TCP","add":"`+testHost+`","port":8443,"id":"`+testUUID+`"}`),
		"trojan://dummy-password@" + testHost + ":443?sni=www.example.com&type=grpc&path=svc#RT-Trojan",
		"trojan://dummy-password@" + testHost + ":8443#RT-Trojan-TCP",
	}

	for _, link := range links {
		t.Run(link, func(t *testing.T) {
			first, err := ParseShareLink(link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			rendered := first.ShareLink()
			if rendered == "" {
				t.Fatal("ShareLink() = \"\"")
			}
			second, err := ParseShareLink(rendered)
			if err != nil {
				t.Fatalf("ParseShareLink(%q): %v", rendered, err)
			}
			if !reflect.DeepEqual(*first, *second) {
				t.Errorf("round trip through %q changed the server\nfirst:  %#v\nsecond: %#v", rendered, *first, *second)
			}
			if got := second.ShareLink(); got != rendered {
				t.Errorf("ShareLink() is not stable: %q then %q", rendered, got)
			}
		})
	}
}

func TestShareLinkEncodesSecretsAndNames(t *testing.T) {
	srv := Server{
		Name:     "Node With Space",
		Type:     TypeHysteria2,
		Address:  testHost,
		Port:     8443,
		Password: "p@ss/word:with+chars",
		SNI:      "www.example.com",
	}
	srv.Normalize()

	link := srv.ShareLink()
	if strings.Contains(link, "p@ss/word") {
		t.Errorf("ShareLink() = %q, want the password percent-encoded", link)
	}
	if !strings.Contains(link, "#Node%20With%20Space") {
		t.Errorf("ShareLink() = %q, want the fragment percent-encoded", link)
	}

	back, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink(%q): %v", link, err)
	}
	if back.Password != srv.Password {
		t.Errorf("Password = %q, want %q", back.Password, srv.Password)
	}
	if back.Name != srv.Name {
		t.Errorf("Name = %q, want %q", back.Name, srv.Name)
	}
}

func TestShareLinkUnknownType(t *testing.T) {
	srv := Server{Name: "x", Type: "wireguard", Address: testHost, Port: 443}
	if got := srv.ShareLink(); got != "" {
		t.Errorf("ShareLink() = %q, want \"\" for an unsupported type", got)
	}
	if _, err := srv.URL(); err == nil {
		t.Error("URL() = nil error, want a failure for an unsupported type")
	}
}

func TestStringNeverLeaksSecrets(t *testing.T) {
	srv := &Server{
		Name: "Secret Node", Type: TypeHysteria2, Address: testHost, Port: 8443,
		Password: testPW,
	}
	srv.Normalize()
	s := srv.String()
	if strings.Contains(s, testPW) {
		t.Errorf("String() = %q, must not contain the password", s)
	}

	vless := &Server{
		Name: "V", Type: TypeVLESS, Address: testHost, Port: 443,
		UUID: testUUID, Security: SecurityReality, SNI: "www.example.com", PublicKey: testPBK,
	}
	vless.Normalize()
	if s := vless.String(); strings.Contains(s, testUUID) || strings.Contains(s, testPBK) {
		t.Errorf("String() = %q, must not contain the uuid or public key", s)
	}
}
