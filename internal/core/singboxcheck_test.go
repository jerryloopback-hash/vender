package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ll-lite/vender/internal/config"
)

// findSingBox locates a real sing-box binary for the tests that pin the engine's
// own opinion of the generated configuration. It returns "" when none is
// available, which makes the caller skip: `go test ./...` must pass on a clean
// clone that has not run scripts/fetch-sing-box.sh, but the check has to be
// possible wherever a developer *does* have the pinned engine.
func findSingBox() string {
	if p := os.Getenv("VENDER_TEST_SINGBOX"); p != "" {
		return p
	}
	name := "sing-box"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// The repository keeps the fetched binaries in dist/sing-box/<goos>-<goarch>/.
	// `go test` runs with the package directory as the working directory, so
	// walking up to the module root (the directory holding go.mod) finds it.
	if root := moduleRoot(); root != "" {
		cand := filepath.Join(root, "dist", "sing-box", runtime.GOOS+"-"+runtime.GOARCH, name)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// moduleRoot walks up from the working directory looking for go.mod.
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// TestGeneratedConfigsPassSingBoxCheck runs the pinned engine's own validator
// over every mode, with the TUN inbound off and on. It is the only test that can
// catch a field name the engine does not know — a golden file, by construction,
// only agrees with itself.
func TestGeneratedConfigsPassSingBoxCheck(t *testing.T) {
	singbox := findSingBox()
	if singbox == "" {
		t.Skip("no sing-box binary (set VENDER_TEST_SINGBOX, or run scripts/fetch-sing-box.sh)")
	}

	srv := checkTestServer()
	for _, mode := range config.ProxyModes() {
		for _, tun := range []bool{false, true} {
			name := string(mode) + "/tun=" + boolText(tun)
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				rc := RuntimeConfig{
					Config:     *config.Default(),
					Server:     srv,
					BinaryPath: singbox,
					RunDir:     dir,
				}
				rc.Config.Proxy.Mode = mode
				rc.Config.Engine.Tun.Enabled = tun
				if mode == config.ModeDirect {
					rc.Server = nil
				}
				if tun {
					// A stack and an exclusion of each kind, so the check also
					// covers route_exclude_address_set resolution against the
					// declared rule sets.
					rc.Config.Engine.Tun.RouteExcludeAddress = []string{"198.18.0.0/15"}
					if mode == config.ModeRule {
						rc.Config.Engine.Tun.RouteExcludeAddressSet = []string{"geoip-cn"}
					} else {
						rc.Config.Engine.Tun.RouteExcludeAddressSet = []string{"geosite-cn"}
					}
				}

				eng, err := New(rc)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if err := eng.WriteConfig(rc); err != nil {
					t.Fatalf("WriteConfig: %v", err)
				}

				out, err := exec.Command(singbox, "check", "-c", ConfigPath(dir)).CombinedOutput()
				if err != nil {
					t.Fatalf("sing-box check rejected the generated %s config: %v\n%s", name, err, out)
				}
			})
		}
	}
}

func boolText(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// checkTestServers returns one server per supported outbound type, each shaped
// the way a real subscription link produces it. They are used by the engine
// acceptance test for M7's new protocols.
func checkTestServers() []*config.Server {
	vmess := &config.Server{
		Name: "Check-VMess", Type: config.TypeVMess, Address: testHost, Port: 443,
		UUID: testUUID, Cipher: "auto", Security: config.SecurityTLS,
		SNI: "www.example.com", Fingerprint: "chrome",
		Network: config.NetworkWS, Path: "/vmess", Host: "www.example.com",
	}
	vmess.Normalize()
	trojan := &config.Server{
		Name: "Check-Trojan", Type: config.TypeTrojan, Address: testHost, Port: 443,
		Password: "check-test-password", Security: config.SecurityTLS,
		SNI: "www.example.com", Insecure: true, ALPN: []string{"h2", "http/1.1"},
	}
	trojan.Normalize()
	ss := &config.Server{
		Name: "Check-SS", Type: config.TypeShadowsocks, Address: testHost, Port: 8388,
		Method: "aes-256-gcm", Password: "check-test-password",
	}
	ss.Normalize()
	ss2022 := &config.Server{
		Name: "Check-SS2022", Type: config.TypeShadowsocks, Address: testHost, Port: 8389,
		Method: "2022-blake3-aes-256-gcm", Password: ss2022Key,
	}
	ss2022.Normalize()
	return []*config.Server{checkTestServer(), checkTestHy2(), ss, ss2022, vmess, trojan}
}

// TestNewOutboundTypesPassSingBoxCheck runs the engine's own validator over a
// config rendered for each M7 outbound type, singly and inside an auto-failover
// group. A golden file can only agree with itself; `sing-box check` is what
// proves the field names and value shapes are ones the pinned engine accepts.
func TestNewOutboundTypesPassSingBoxCheck(t *testing.T) {
	singbox := findSingBox()
	if singbox == "" {
		t.Skip("no sing-box binary (set VENDER_TEST_SINGBOX, or run scripts/fetch-sing-box.sh)")
	}

	for _, srv := range checkTestServers() {
		srv := srv
		t.Run(string(srv.Type)+"/"+srv.Name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := *config.Default()
			rc := RuntimeConfig{Config: cfg, Server: srv, BinaryPath: singbox, RunDir: dir}
			eng, err := New(rc)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := eng.WriteConfig(rc); err != nil {
				t.Fatalf("WriteConfig: %v", err)
			}
			out, err := exec.Command(singbox, "check", "-c", ConfigPath(dir)).CombinedOutput()
			if err != nil {
				t.Fatalf("sing-box check rejected the %s config: %v\n%s", srv.Type, err, out)
			}
		})
	}

	t.Run("auto-failover-mixed-profile", func(t *testing.T) {
		dir := t.TempDir()
		cfg := *config.Default()
		cfg.SelectedServer = config.SelectedServerAuto
		pool := make([]config.Server, 0, len(checkTestServers()))
		for _, s := range checkTestServers() {
			pool = append(pool, *s)
		}
		rc := RuntimeConfig{Config: cfg, Servers: pool, BinaryPath: singbox, RunDir: dir}
		eng, err := New(rc)
		if err != nil {
			t.Fatalf("New (auto): %v", err)
		}
		if err := eng.WriteConfig(rc); err != nil {
			t.Fatalf("WriteConfig (auto): %v", err)
		}
		out, err := exec.Command(singbox, "check", "-c", ConfigPath(dir)).CombinedOutput()
		if err != nil {
			t.Fatalf("sing-box check rejected the mixed auto config: %v\n%s", err, out)
		}
	})
}

// checkTestHy2 is a Hysteria2 server the engine accepts, used as the second
// member of an auto-failover group.
func checkTestHy2() *config.Server {
	return &config.Server{
		Name: "Check-Hy2", Type: config.TypeHysteria2, Address: testHost, Port: 8443,
		Password: "check-test-password", SNI: "www.example.com", Insecure: true,
	}
}

// TestAutoConfigPassesSingBoxCheck pins the failover group: every server becomes
// its own outbound, wrapped in a URLTest group tagged `proxy`, and the route
// final still names `proxy`. It covers rule and global mode, because in global
// mode the DNS resolver is reached *through* the group, which is the combination
// most likely to be rejected.
func TestAutoConfigPassesSingBoxCheck(t *testing.T) {
	singbox := findSingBox()
	if singbox == "" {
		t.Skip("no sing-box binary (set VENDER_TEST_SINGBOX, or run scripts/fetch-sing-box.sh)")
	}

	for _, mode := range []config.ProxyMode{config.ModeRule, config.ModeGlobal} {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			cfg := *config.Default()
			cfg.Proxy.Mode = mode
			cfg.SelectedServer = config.SelectedServerAuto
			// A non-default interval and tolerance, so the check also covers
			// the engine's own acceptance of the rendered knobs.
			cfg.Engine.FailoverInterval = "45s"
			cfg.Engine.FailoverToleranceMS = 250
			rc := RuntimeConfig{
				Config:     cfg,
				Servers:    []config.Server{*checkTestServer(), *checkTestHy2()},
				BinaryPath: singbox,
				RunDir:     dir,
			}
			eng, err := New(rc)
			if err != nil {
				t.Fatalf("New (auto): %v", err)
			}
			if err := eng.WriteConfig(rc); err != nil {
				t.Fatalf("WriteConfig (auto): %v", err)
			}
			out, err := exec.Command(singbox, "check", "-c", ConfigPath(dir)).CombinedOutput()
			if err != nil {
				t.Fatalf("sing-box check rejected the auto %s config: %v\n%s", mode, err, out)
			}
		})
	}
}

// checkTestServer is a REALITY server whose fields the engine accepts. The
// golden fixtures use obviously fake values ("DUMMY-PUBLIC-KEY") because they
// never leave vender, but sing-box validates the REALITY public key, so this
// one carries a real — and disposable — x25519 public key, generated with
// `sing-box generate reality-keypair` and used by no server anywhere.
func checkTestServer() *config.Server {
	return &config.Server{
		Name: "Check-Reality", Type: config.TypeVLESS, Address: testHost, Port: 443,
		UUID: testUUID, Flow: "xtls-rprx-vision", Security: config.SecurityReality,
		SNI: "www.example.com", Fingerprint: "chrome",
		PublicKey: "MvrfukqMMpI1wywKwUvTacjfSkBEvC6CutkwPDf7XF8",
		ShortID:   "0123456789abcdef",
	}
}

// TestProfileDNSPassesSingBoxCheck runs the engine's own validator over the
// config a profile DNS policy produces. It is the check that matters most for
// this feature: `domain_resolver` on an outbound, a `tcp` DNS server reached by
// name, and the `local` bootstrap are all fields whose exact name and placement
// only the pinned binary can confirm.
func TestProfileDNSPassesSingBoxCheck(t *testing.T) {
	singbox := findSingBox()
	if singbox == "" {
		t.Skip("no sing-box binary (set VENDER_TEST_SINGBOX, or run scripts/fetch-sing-box.sh)")
	}

	policy := &config.ProfileDNS{NamespacePolicy: []config.DNSNamespacePolicy{
		{Domain: "v51124-6.qpon", Server: "tcp://dns.provider.example:8080"},
		{Domain: "1.2.3.4.in-addr.arpa", Server: "https://dns.example/dns-query"},
	}}
	policy.Normalize()
	ss := &config.Server{
		Name: "Check-Entry-SS", Type: config.TypeShadowsocks,
		Address: "1.k.o.5.hk01-ae5.entry.v51124-6.qpon", Port: 19276,
		Method: "aes-256-gcm", Password: "check-test-password", Profile: "provider",
	}
	ss.Normalize()

	for _, mode := range []config.ProxyMode{config.ModeRule, config.ModeGlobal} {
		for _, auto := range []bool{false, true} {
			name := string(mode) + "/auto=" + boolText(auto)
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				cfg := *config.Default()
				cfg.Proxy.Mode = mode
				if auto {
					cfg.SelectedServer = config.SelectedServerAuto
				}
				rc := RuntimeConfig{
					Config:     cfg,
					Server:     ss,
					ProfileDNS: map[string]*config.ProfileDNS{"provider": policy},
					BinaryPath: singbox,
					RunDir:     dir,
				}
				if auto {
					rc.Server = nil
					rc.Servers = []config.Server{*ss}
				}
				eng, err := New(rc)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if err := eng.WriteConfig(rc); err != nil {
					t.Fatalf("WriteConfig: %v", err)
				}
				out, err := exec.Command(singbox, "check", "-c", ConfigPath(dir)).CombinedOutput()
				if err != nil {
					t.Fatalf("sing-box check rejected the profile-DNS %s config: %v\n%s", name, err, out)
				}
			})
		}
	}
}
