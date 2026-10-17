package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
)

// subTransport answers a subscription request without a socket, so the CLI
// tests stay hermetic. The body is built from the same dummy links the
// subscription package's fixtures use.
type subTransport struct {
	status   int
	body     string
	lastUA   string
	lastPath string
}

func (s *subTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.lastUA = req.Header.Get("User-Agent")
	s.lastPath = req.URL.Path
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
	}, nil
}

// testSubscription is a synthetic subscription: a mix of the M7 protocols,
// base64-encoded the way a v2ray panel serves them.
func testSubscription() string {
	links := strings.Join([]string{
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:dummy-ss-pass")) + "@203.0.113.20:8388#Sub-SS",
		"trojan://dummy-trojan-pass@203.0.113.21:443?sni=www.example.com#Sub-Trojan",
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(
			`{"v":"2","ps":"Sub-VMess","add":"203.0.113.22","port":"443","id":"`+testUUID+`","aid":"0","scy":"auto","net":"ws","tls":"tls","sni":"cdn.example.com","path":"/ray","host":"cdn.example.com"}`)),
		"hysteria2://dummy-hy2-pass@203.0.113.23:8443/?sni=www.example.com#Sub-Hy2",
		"wireguard://nope@203.0.113.24:51820#Sub-Unsupported",
	}, "\n") + "\n"
	return base64.StdEncoding.EncodeToString([]byte(links))
}

// withSubscriptionTransport replaces the subscription HTTP client for one test.
func withSubscriptionTransport(t *testing.T, body string, status int) *subTransport {
	t.Helper()
	ft := &subTransport{status: status, body: body}
	prev := subscriptionClient
	subscriptionClient = func() *http.Client { return &http.Client{Transport: ft} }
	t.Cleanup(func() { subscriptionClient = prev })
	return ft
}

func TestCLIImportSubscriptionURL(t *testing.T) {
	dir := t.TempDir()
	ft := withSubscriptionTransport(t, testSubscription(), 200)

	out, err := runCLI(t, "import", "--url", "https://sub.example.com/api/v1/client/subscribe?token=SECRET", "--config-dir", dir)
	if err != nil {
		t.Fatalf("import --url: %v\n%s", err, out)
	}
	if !strings.HasPrefix(ft.lastUA, "v2rayN/") {
		t.Errorf("User-Agent = %q, want a v2rayN agent", ft.lastUA)
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("the subscription token leaked into the output:\n%s", out)
	}
	for _, want := range []string{"Profile \"sub.example.com\"", "added 4", "skipped 1", "profiles use sub.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("import output does not mention %q:\n%s", want, out)
		}
	}

	ss, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatal(err)
	}
	if ss.Len() != 4 {
		t.Fatalf("servers.yaml has %d entries, want 4 (%v)", ss.Len(), ss.Names())
	}
	for i := range ss.Servers {
		if ss.Servers[i].Profile != "sub.example.com" {
			t.Errorf("%s belongs to profile %q", ss.Servers[i].Name, ss.Servers[i].Profile)
		}
	}
	// No server was auto-selected: making the profile active is explicit.
	cfg, err := config.NewStoreAt(dir).LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SelectedServer != "" {
		t.Errorf("selected_server = %q, want empty", cfg.SelectedServer)
	}
	if _, err := os.Stat(filepath.Join(dir, config.ProfilesFileName)); err != nil {
		t.Errorf("profiles.yaml was not written: %v", err)
	}
}

func TestCLIImportSubscriptionRefusesASecondName(t *testing.T) {
	dir := t.TempDir()
	withSubscriptionTransport(t, testSubscription(), 200)
	if _, err := runCLI(t, "import", "--url", "https://sub.example.com/x", "--config-dir", dir); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := runCLI(t, "import", "ss://"+base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:p"))+"@203.0.113.30:8388#Sub-SS", "--config-dir", dir); err == nil {
		t.Error("a manual import of a name another profile holds = nil error, want a duplicate error")
	}
	// With --force it replaces.
	if _, err := runCLI(t, "import", "--force", "ss://"+base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:p"))+"@203.0.113.30:8388#Sub-SS", "--config-dir", dir); err != nil {
		t.Errorf("forced import: %v", err)
	}
}

func TestCLIProfilesLifecycle(t *testing.T) {
	dir := t.TempDir()
	withSubscriptionTransport(t, testSubscription(), 200)
	if _, err := runCLI(t, "import", "--url", "https://sub.example.com/x", "--profile", "provider", "--name-prefix", "P-", "--config-dir", dir); err != nil {
		t.Fatalf("import --url: %v", err)
	}
	// A hand-imported node, so the manual profile is not empty.
	if _, err := runCLI(t, "import", hy2Link, "--config-dir", dir); err != nil {
		t.Fatalf("import link: %v", err)
	}

	out, err := runCLI(t, "profiles", "list", "--config-dir", dir)
	if err != nil {
		t.Fatalf("profiles list: %v", err)
	}
	for _, want := range []string{"manual", "provider", "sub.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("profiles list does not mention %q:\n%s", want, out)
		}
	}
	// The manual profile is the active one by default, and holds only the
	// hand-imported node.
	if !strings.Contains(out, "active profile: manual") {
		t.Errorf("profiles list = %s", out)
	}

	// The subscription nodes were imported under the prefix, so the hand
	// imported node and the provider's node can coexist.
	sout, err := runCLI(t, "servers", "list", "--config-dir", dir)
	if err != nil {
		t.Fatalf("servers list: %v", err)
	}
	for _, want := range []string{"P-Sub-SS", "P-Sub-VMess", "Test-Hy2", "shadowsocks", "vmess"} {
		if !strings.Contains(sout, want) {
			t.Errorf("servers list does not mention %q:\n%s", want, sout)
		}
	}

	out, err = runCLI(t, "profiles", "use", "provider", "--config-dir", dir)
	if err != nil {
		t.Fatalf("profiles use: %v", err)
	}
	if !strings.Contains(out, "active profile = provider") {
		t.Errorf("profiles use = %s", out)
	}
	// Now select auto and check the selection is the profile-scoped sentinel.
	if _, err := runCLI(t, "servers", "use", "auto", "--config-dir", dir); err != nil {
		t.Fatalf("servers use auto: %v", err)
	}
	ps, err := config.NewStoreAt(dir).LoadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if ps.ActiveName() != "provider" {
		t.Errorf("active profile = %q", ps.ActiveName())
	}

	// Removing a profile removes its servers and leaves the hand-imported one.
	out, err = runCLI(t, "profiles", "rm", "provider", "--config-dir", dir)
	if err != nil {
		t.Fatalf("profiles rm: %v", err)
	}
	if !strings.Contains(out, "Removed profile \"provider\" and 4 server(s)") {
		t.Errorf("profiles rm = %s", out)
	}
	ss, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatal(err)
	}
	if ss.Len() != 1 || ss.Servers[0].Name != "Test-Hy2" {
		t.Errorf("after removal: %v", ss.Names())
	}
	ps, _ = config.NewStoreAt(dir).LoadProfiles()
	if ps.ActiveName() != config.ProfileManual {
		t.Errorf("active profile after removal = %q", ps.ActiveName())
	}

	// The built-in profile is the guaranteed last one.
	if _, err := runCLI(t, "profiles", "rm", "manual", "--config-dir", dir); err == nil {
		t.Error("profiles rm manual = nil error, want a refusal")
	}
	if _, err := runCLI(t, "profiles", "use", "ghost", "--config-dir", dir); err == nil {
		t.Error("profiles use ghost = nil error, want a not-found error")
	}
}

func TestCLIProfilesUpdate(t *testing.T) {
	dir := t.TempDir()
	ft := withSubscriptionTransport(t, testSubscription(), 200)
	if _, err := runCLI(t, "import", "--url", "https://sub.example.com/x", "--profile", "provider", "--config-dir", dir); err != nil {
		t.Fatalf("import --url: %v", err)
	}

	// The subscription changes: one node disappears, one is new.
	ft.body = base64.StdEncoding.EncodeToString([]byte(
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:dummy-ss-pass")) + "@203.0.113.20:8388#Sub-SS\n" +
			"trojan://dummy-trojan-pass@203.0.113.99:443?sni=www.example.com#Sub-Trojan-New\n"))

	out, err := runCLI(t, "profiles", "update", "provider", "--config-dir", dir)
	if err != nil {
		t.Fatalf("profiles update: %v\n%s", err, out)
	}
	for _, want := range []string{"added 1", "removed 3", "Sub-Trojan"} {
		if !strings.Contains(out, want) {
			t.Errorf("update output does not mention %q:\n%s", want, out)
		}
	}
	ss, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatal(err)
	}
	if got := ss.Names(); len(got) != 2 {
		t.Errorf("after update: %v", got)
	}
	if !strings.Contains(strings.Join(ss.Names(), ","), "Sub-Trojan-New") {
		t.Errorf("the new node is missing: %v", ss.Names())
	}

	// Updating a profile with no URL is refused.
	if _, err := runCLI(t, "profiles", "update", "manual", "--config-dir", dir); err == nil {
		t.Error("profiles update manual = nil error, want a refusal")
	}
	if _, err := runCLI(t, "profiles", "update", "ghost", "--config-dir", dir); err == nil {
		t.Error("profiles update ghost = nil error, want a refusal")
	}
}

func TestCLIImportSubscriptionErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, "import", "--url", "https://sub.example.com/x", "--config-dir", dir); err == nil {
		t.Error("no transport configured = nil error, want a fetch failure")
	}
	withSubscriptionTransport(t, "", 500)
	if _, err := runCLI(t, "import", "--url", "https://sub.example.com/x", "--config-dir", dir); err == nil {
		t.Error("HTTP 500 = nil error, want a failure")
	}
	withSubscriptionTransport(t, "<html>a clash page that is not proxies</html>", 200)
	if _, err := runCLI(t, "import", "--url", "https://sub.example.com/x", "--config-dir", dir); err == nil {
		t.Error("an unparseable body = nil error, want a failure")
	}
	// Both a link and --url is a mistake, not a silent override.
	withSubscriptionTransport(t, testSubscription(), 200)
	if _, err := runCLI(t, "import", hy2Link, "--url", "https://sub.example.com/x", "--config-dir", dir); err == nil {
		t.Error("a link plus --url = nil error, want a refusal")
	}
}
