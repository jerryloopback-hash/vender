package subscription

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
)

// The fixtures under testdata/ are dummies only: 203.0.113.0/24 (TEST-NET-3),
// fake UUIDs and example.com. They are the three subscription shapes a panel
// serves, plus the nodes vender must skip with a reason.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestParsePlainLinks(t *testing.T) {
	res, format, err := Parse(readFixture(t, "links.txt"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if format != FormatLinks {
		t.Errorf("format = %q, want %q", format, FormatLinks)
	}
	// Six usable nodes: ss (both forms), vmess, trojan, vless, hysteria2.
	if len(res.Servers) != 6 {
		t.Fatalf("parsed %d servers, want 6 (%v)", len(res.Servers), serverNames(res.Servers))
	}
	wantTypes := map[string]config.ServerType{
		"Provider-SS-A":      config.TypeShadowsocks,
		"Provider-SS-Legacy": config.TypeShadowsocks,
		"Provider-VMess":     config.TypeVMess,
		"Provider-Trojan":    config.TypeTrojan,
		"Provider-VLESS":     config.TypeVLESS,
		"Provider-Hy2":       config.TypeHysteria2,
	}
	for _, s := range res.Servers {
		if wantTypes[s.Name] != s.Type {
			t.Errorf("%s: type = %q, want %q", s.Name, s.Type, wantTypes[s.Name])
		}
	}
	if s := findServer(t, res.Servers, "Provider-VMess"); s.Path != "/ray" || s.Network != config.NetworkWS || s.Security != config.SecurityTLS {
		t.Errorf("vmess node = %#v", *s)
	}

	// The plugin node and the unsupported scheme are skipped, with reasons.
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped %d nodes, want 2 (%+v)", len(res.Skipped), res.Skipped)
	}
	reasons := strings.ToLower(skipReasons(res.Skipped))
	if !strings.Contains(reasons, "plugin") {
		t.Errorf("no plugin skip reported: %+v", res.Skipped)
	}
	if !strings.Contains(reasons, "unsupported share link scheme") {
		t.Errorf("no unsupported-scheme skip reported: %+v", res.Skipped)
	}
}

func TestParseBase64Links(t *testing.T) {
	raw := readFixture(t, "links.txt")
	encoded := base64.StdEncoding.EncodeToString(raw)
	res, format, err := Parse([]byte(encoded))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if format != FormatBase64Links {
		t.Errorf("format = %q, want %q", format, FormatBase64Links)
	}
	if len(res.Servers) != 6 {
		t.Errorf("parsed %d servers, want 6", len(res.Servers))
	}

	// The URL-safe, unpadded spelling panels sometimes use.
	alt := base64.RawURLEncoding.EncodeToString(raw)
	if _, format, err := Parse([]byte(alt)); err != nil || format != FormatBase64Links {
		t.Errorf("URL-safe base64: format = %q, err = %v", format, err)
	}
}

func TestParseClashYAML(t *testing.T) {
	res, format, err := Parse(readFixture(t, "clash.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if format != FormatClash {
		t.Errorf("format = %q, want %q", format, FormatClash)
	}
	if len(res.Servers) != 5 {
		t.Fatalf("parsed %d servers, want 5 (%v)", len(res.Servers), serverNames(res.Servers))
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped %d, want 2 (%+v)", len(res.Skipped), res.Skipped)
	}

	ss := findServer(t, res.Servers, "Clash-SS")
	if ss.Method != "aes-256-gcm" || ss.Password != "dummy-ss-pass" {
		t.Errorf("Clash-SS = %#v", *ss)
	}
	vm := findServer(t, res.Servers, "Clash-VMess-WS")
	if vm.UUID == "" || vm.Network != config.NetworkWS || vm.Path != "/ray" ||
		vm.Host != "cdn.example.com" || vm.Security != config.SecurityTLS || vm.Fingerprint != "chrome" {
		t.Errorf("Clash-VMess-WS = %#v", *vm)
	}
	tr := findServer(t, res.Servers, "Clash-Trojan")
	if tr.Password != "dummy-trojan-pass" || !tr.Insecure || tr.Security != config.SecurityTLS || len(tr.ALPN) != 2 {
		t.Errorf("Clash-Trojan = %#v", *tr)
	}
	vl := findServer(t, res.Servers, "Clash-VLESS-Reality")
	if vl.Security != config.SecurityReality || vl.PublicKey == "" || vl.Flow != "xtls-rprx-vision" {
		t.Errorf("Clash-VLESS-Reality = %#v", *vl)
	}
	hy := findServer(t, res.Servers, "Clash-Hysteria2")
	if hy.Type != config.TypeHysteria2 || hy.Password != "dummy-hy2-pass" {
		t.Errorf("Clash-Hysteria2 = %#v", *hy)
	}

	// Every node that ends up in the list must validate, which is what makes
	// it safe to merge into servers.yaml.
	for i := range res.Servers {
		if err := res.Servers[i].Validate(); err != nil {
			t.Errorf("%s does not validate: %v", res.Servers[i].Name, err)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, body := range []string{"", "   ", "<html><body>nope</body></html>", "not a subscription"} {
		if _, _, err := Parse([]byte(body)); err == nil {
			t.Errorf("Parse(%q) = nil error, want a failure", body)
		}
	}
}

func TestParseDropsDuplicateNames(t *testing.T) {
	links := "hysteria2://a@203.0.113.10:8443/#dup\nhysteria2://b@203.0.113.11:8443/#dup\n"
	res := ParseLinks(links)
	if len(res.Servers) != 1 {
		t.Errorf("parsed %d, want 1 after de-duplication", len(res.Servers))
	}
	if len(res.Skipped) != 1 || !strings.Contains(strings.ToLower(res.Skipped[0].Reason), "duplicate") {
		t.Errorf("skipped = %+v", res.Skipped)
	}
}

func TestDeriveProfileName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://sub.example.com/api/v1/client/subscribe?token=x", "sub.example.com"},
		{"https://www.example.com/x", "example.com"},
		{"http://203.0.113.10:8080/sub", "203.0.113.10"},
		{"", "sub"},
		{"not a url", "sub"},
	} {
		if got := DeriveProfileName(tc.in); got != tc.want {
			t.Errorf("DeriveProfileName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// fakeTransport answers a request without touching a socket, so the fetch tests
// are hermetic (no network, not even loopback).
type fakeTransport struct {
	status int
	body   string
	lastUA string
	url    string
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.lastUA = req.Header.Get("User-Agent")
	f.url = req.URL.String()
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func TestFetchSendsTheV2RayUserAgent(t *testing.T) {
	ft := &fakeTransport{status: 200, body: "vless://x\n"}
	client := &http.Client{Transport: ft}
	body, err := Fetch(context.Background(), client, "https://sub.example.com/api?token=secret")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != "vless://x\n" {
		t.Errorf("body = %q", body)
	}
	if !strings.HasPrefix(ft.lastUA, "v2rayN/") {
		t.Errorf("User-Agent = %q, want a v2rayN agent so panels return links", ft.lastUA)
	}
}

func TestFetchErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Fetch(ctx, nil, "ftp://example.com/x"); err == nil {
		t.Error("a non-http(s) URL was accepted")
	}
	if _, err := Fetch(ctx, nil, ""); err == nil {
		t.Error("an empty URL was accepted")
	}
	ft := &fakeTransport{status: 404, body: "nope"}
	if _, err := Fetch(ctx, &http.Client{Transport: ft}, "https://sub.example.com/x"); err == nil {
		t.Error("HTTP 404 was accepted")
	}
}

func serverNames(servers []config.Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

func skipReasons(skipped []Skipped) string {
	var b strings.Builder
	for _, s := range skipped {
		b.WriteString(s.Reason)
		b.WriteString("\n")
	}
	return b.String()
}

func findServer(t *testing.T, servers []config.Server, name string) *config.Server {
	t.Helper()
	for i := range servers {
		if servers[i].Name == name {
			return &servers[i]
		}
	}
	t.Fatalf("no server named %q in %v", name, serverNames(servers))
	return nil
}

// A Clash file whose provider states a DNS policy: vender must capture
// dns.nameserver-policy (both the +. and *. suffix forms, every resolver
// scheme, and multiple servers for one domain), keep the default lists for
// auditing, and drop a rule-set-keyed policy it cannot match a hostname
// against.
func TestParseClashCapturesDNSPolicy(t *testing.T) {
	res, format, err := Parse(readFixture(t, "clash_dns.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if format != FormatClash {
		t.Fatalf("format = %q, want %q", format, FormatClash)
	}
	dns := res.DNS
	if dns == nil {
		t.Fatal("ParseClash captured no DNS policy")
	}
	want := []config.DNSNamespacePolicy{
		// The first server of a multi-server rule is used; the rest stay in Raw.
		{Domain: "v51124-6.qpon", Server: "tcp://dns.provider.example:8080"},
		{Domain: "rtysjur.quest", Server: "https://doh.provider.example/dns-query"},
		{Domain: "legacy.entry", Server: "203.0.113.53"},
		{Domain: "multi.entry", Server: "udp://resolver1.example:5353"},
	}
	if len(dns.NamespacePolicy) != len(want) {
		t.Fatalf("namespace_policy = %+v, want %+v", dns.NamespacePolicy, want)
	}
	for i := range want {
		if dns.NamespacePolicy[i] != want[i] {
			t.Errorf("namespace_policy[%d] = %+v, want %+v", i, dns.NamespacePolicy[i], want[i])
		}
	}
	if len(dns.Nameservers) != 2 || dns.Nameservers[0] != "119.29.29.29" {
		t.Errorf("nameservers = %v, want the two captured defaults", dns.Nameservers)
	}
	if len(dns.Fallback) != 1 || dns.Fallback[0] != "8.8.8.8" {
		t.Errorf("fallback = %v, want [8.8.8.8]", dns.Fallback)
	}
	for _, want := range []string{"nameserver-policy", "nameserver", "fallback", "geosite:cn"} {
		if !strings.Contains(dns.Raw, want) {
			t.Errorf("Raw does not keep %q:\n%s", want, dns.Raw)
		}
	}

	// The matched node resolves through the provider; the rtysjur node through
	// the *. rule; a node under no pattern through nothing.
	if got := dns.Match("1.k.o.5.hk01-ae5.entry.v51124-6.qpon"); got != "tcp://dns.provider.example:8080" {
		t.Errorf("Match(qpon node) = %q", got)
	}
	if got := dns.Match("1kw2i0k.g06.jp02.entry.rtysjur.quest"); got != "https://doh.provider.example/dns-query" {
		t.Errorf("Match(rtysjur node) = %q", got)
	}
	if got := dns.Match("node.example.net"); got != "" {
		t.Errorf("Match(unmatched) = %q, want empty", got)
	}
}

// A Clash file with no dns block, and a link list, carry no policy: an absent
// policy must stay nil so it renders nothing.
func TestSubscriptionsWithoutDNSHaveNoPolicy(t *testing.T) {
	res, _, err := Parse(readFixture(t, "clash.yaml"))
	if err != nil {
		t.Fatalf("Parse(clash): %v", err)
	}
	if res.DNS != nil {
		t.Errorf("a Clash file without a dns block produced a policy: %+v", res.DNS)
	}

	links, _, err := Parse(readFixture(t, "links.txt"))
	if err != nil {
		t.Fatalf("Parse(links): %v", err)
	}
	if links.DNS != nil {
		t.Errorf("a link list produced a policy: %+v", links.DNS)
	}
}
