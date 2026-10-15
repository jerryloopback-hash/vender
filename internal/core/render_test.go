package core

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

// goldenCase is one rendering scenario: a RuntimeConfig plus the golden file
// its sing-box JSON must match byte for byte.
type goldenCase struct {
	name string
	rc   RuntimeConfig
	file string
}

func goldenCases(t *testing.T) []goldenCase {
	t.Helper()

	reality := testVLESS()

	hy2 := &config.Server{
		Name: "Test-Hy2", Type: config.TypeHysteria2, Address: testHost, Port: 8443,
		Password: testPW, Obfs: "salamander", ObfsPassword: "cafebabe",
		SNI: "www.bing.com", Insecure: true, ALPN: []string{"h3"},
	}
	plainTLS := &config.Server{
		Name: "Test-TLS", Type: config.TypeVLESS, Address: testHost, Port: 443,
		UUID: testUUID, Security: config.SecurityTLS, SNI: "www.example.com",
		Fingerprint: "firefox",
	}

	return []goldenCase{
		{
			name: "vless-reality",
			file: "vless_reality.golden",
			rc: RuntimeConfig{
				// bind_interface is a global engine setting, so it exercises
				// the config -> outbound plumbing as well.
				Config: func() config.Config {
					c := *config.Default()
					c.Engine.BindInterface = "wlp0s20f3"
					return c
				}(),
				Server: reality, RunDir: "/run/vender",
			},
		},
		{
			name: "hysteria2",
			file: "hysteria2.golden",
			rc: RuntimeConfig{
				Config: func() config.Config {
					c := *config.Default()
					c.Proxy.Mode = config.ModeGlobal
					return c
				}(),
				Server: hy2, RunDir: "/run/vender",
			},
		},
		{
			name: "direct-mode",
			file: "direct.golden",
			rc: RuntimeConfig{
				Config: func() config.Config {
					c := *config.Default()
					c.Proxy.Mode = config.ModeDirect
					c.Proxy.Listen = "127.0.0.1:7897"
					c.LogLevel = config.LogLevelWarn
					return c
				}(),
				RunDir: "/run/vender",
			},
		},
		{
			name: "vless-tls-plain",
			file: "vless_tls.golden",
			rc: RuntimeConfig{
				Config: *config.Default(), Server: plainTLS, RunDir: "/run/vender",
			},
		},
		{
			name: "hysteria2-minimal",
			file: "hysteria2_minimal.golden",
			rc: RuntimeConfig{
				Config: *config.Default(),
				Server: &config.Server{
					Name: "Test-Hy2-Min", Type: config.TypeHysteria2, Address: testHost,
					Port: 8443, Password: testPW, SNI: "www.bing.com",
					PinSHA256: "aabbccdd",
				},
				RunDir: "/run/vender",
			},
		},
	}
}

func TestBuildConfigGolden(t *testing.T) {
	for _, tc := range goldenCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildConfig(tc.rc)
			if err != nil {
				t.Fatalf("BuildConfig: %v", err)
			}
			path := filepath.Join("testdata", tc.file)
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden file (run `go test ./internal/core -update`): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("rendered config differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
			}
		})
	}
}

// The generated config must never carry a deprecated field: sing-box 1.13
// removed the legacy inbound options outright, so emitting one would make the
// engine refuse to start on the pinned version. Sniffing is legitimate only as
// the route *action* rendered in rule mode, never as an inbound key.
func TestBuildConfigCarriesNoLegacyFields(t *testing.T) {
	for _, tc := range goldenCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			data, err := BuildConfig(tc.rc)
			if err != nil {
				t.Fatalf("BuildConfig: %v", err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			var inbounds []map[string]json.RawMessage
			if err := json.Unmarshal(raw["inbounds"], &inbounds); err != nil {
				t.Fatalf("unmarshal inbounds: %v", err)
			}
			allowed := map[string]bool{"type": true, "tag": true, "listen": true, "listen_port": true}
			for _, in := range inbounds {
				for k := range in {
					if !allowed[k] {
						t.Errorf("inbound has unexpected key %q (legacy inbounds fields are rejected by sing-box 1.13+)", k)
					}
				}
			}

			var route struct {
				Rules []map[string]json.RawMessage `json:"rules"`
			}
			if err := json.Unmarshal(raw["route"], &route); err != nil {
				t.Fatalf("unmarshal route: %v", err)
			}
			for i, r := range route.Rules {
				if _, ok := r["sniff"]; ok {
					t.Errorf("route rule %d uses the legacy \"sniff\" key; sniffing is an action in sing-box 1.13+", i)
				}
				if _, ok := r["enable"]; ok {
					t.Errorf("route rule %d uses the legacy \"enable\" key", i)
				}
				_, hasAction := r["action"]
				_, hasOutbound := r["outbound"]
				if hasAction && hasOutbound {
					t.Errorf("route rule %d is both an action and an outbound selection", i)
				}
				if !hasAction && !hasOutbound {
					t.Errorf("route rule %d has neither an action nor an outbound", i)
				}
			}
		})
	}
}

// direct mode must not reference the (possibly missing) selected server at all.
func TestDirectModeHasNoProxyOutbound(t *testing.T) {
	rc := RuntimeConfig{
		Config: func() config.Config {
			c := *config.Default()
			c.Proxy.Mode = config.ModeDirect
			return c
		}(),
		RunDir: "/run/vender",
	}
	data, err := BuildConfig(rc)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var cfg struct {
		Outbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"outbounds"`
		Route struct {
			Final string `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Route.Final != tagDirect {
		t.Errorf("route.final = %q in direct mode, want %q", cfg.Route.Final, tagDirect)
	}
	for _, o := range cfg.Outbounds {
		if o.Tag == tagProxy {
			t.Errorf("direct mode emitted a proxy outbound: %+v", o)
		}
	}
}

func TestBuildConfigRejectsUnsupportedServer(t *testing.T) {
	srv := testVLESS()
	srv.Type = "shadowsocks"
	// Validate() rejects the type first, so BuildConfig must fail too.
	if _, err := BuildConfig(RuntimeConfig{Config: *config.Default(), Server: srv, RunDir: "/run"}); err == nil {
		t.Fatal("BuildConfig with an unsupported type = nil error, want a failure")
	}
}

// Rule mode must declare every rule set its rules and DNS rules reference, and
// nothing else, and the paths must point into the run dir where WriteConfig
// extracts them.
func TestRuleModeDeclaresExactlyTheReferencedRuleSets(t *testing.T) {
	dir := t.TempDir()
	data, err := BuildConfig(RuntimeConfig{Config: *config.Default(), Server: testVLESS(), RunDir: dir})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var cfg struct {
		DNS struct {
			Rules []struct {
				RuleSet []string `json:"rule_set"`
			} `json:"rules"`
		} `json:"dns"`
		Route struct {
			RuleSet []struct {
				Tag  string `json:"tag"`
				Path string `json:"path"`
			} `json:"rule_set"`
			Rules []struct {
				Action   string   `json:"action"`
				RuleSet  []string `json:"rule_set"`
				Outbound string   `json:"outbound"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	declared := map[string]string{}
	for _, rs := range cfg.Route.RuleSet {
		declared[rs.Tag] = rs.Path
	}
	if len(declared) != len(rules.ReferencedRuleSets()) {
		t.Errorf("declared %d rule sets, want %d: %v", len(declared), len(rules.ReferencedRuleSets()), declared)
	}
	for _, tag := range rules.ReferencedRuleSets() {
		path, ok := declared[tag]
		if !ok {
			t.Errorf("rule set %q is referenced but not declared", tag)
			continue
		}
		want := filepath.Join(dir, rules.RulesSubdir, tag+".srs")
		if path != want {
			t.Errorf("rule set %q path = %q, want %q", tag, path, want)
		}
	}

	// Every referenced tag must be declared, from the route rules and from the
	// DNS rules: sing-box looks both up in the same route.rule_set list.
	ipRules := 0
	for _, r := range cfg.Route.Rules {
		if r.Action != "" {
			continue
		}
		if len(r.RuleSet) == 0 {
			// The one destination rule without a rule set is the
			// private-address match, which uses ip_is_private instead.
			ipRules++
			continue
		}
		for _, tag := range r.RuleSet {
			if _, ok := declared[tag]; !ok {
				t.Errorf("route rule references undeclared rule set %q", tag)
			}
		}
	}
	if ipRules != 1 {
		t.Errorf("%d destination rules match without a rule set, want exactly the private one", ipRules)
	}
	if len(cfg.DNS.Rules) == 0 {
		t.Fatal("rule mode emitted no DNS rules, so the split DNS policy is gone")
	}
	for _, r := range cfg.DNS.Rules {
		for _, tag := range r.RuleSet {
			if _, ok := declared[tag]; !ok {
				t.Errorf("DNS rule references undeclared rule set %q", tag)
			}
		}
	}
}

// Global mode means what it says: everything through the proxy. Emitting the
// built-in CN rules there would quietly turn "global" into "rule".
func TestGlobalModeCarriesNoRoutingRules(t *testing.T) {
	cfg := *config.Default()
	cfg.Proxy.Mode = config.ModeGlobal
	data, err := BuildConfig(RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var got struct {
		Route struct {
			Final   string            `json:"final"`
			Rules   []json.RawMessage `json:"rules"`
			RuleSet []json.RawMessage `json:"rule_set"`
		} `json:"route"`
		DNS *struct {
			Servers []struct {
				Tag    string `json:"tag"`
				Detour string `json:"detour"`
			} `json:"servers"`
			Rules []json.RawMessage `json:"rules"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Route.Final != tagProxy {
		t.Errorf("global mode route.final = %q, want proxy", got.Route.Final)
	}
	if len(got.Route.Rules) != 0 {
		t.Errorf("global mode emitted %d routing rules", len(got.Route.Rules))
	}
	if len(got.Route.RuleSet) != 0 {
		t.Errorf("global mode declared %d rule sets", len(got.Route.RuleSet))
	}
	if got.DNS == nil {
		t.Fatal("global mode emitted no dns section, so DNS would resolve locally")
	}
	if len(got.DNS.Rules) != 0 {
		t.Errorf("global mode emitted %d DNS rules", len(got.DNS.Rules))
	}
	if len(got.DNS.Servers) != 1 || got.DNS.Servers[0].Detour != tagProxy {
		t.Errorf("global mode DNS servers = %+v, want a single proxied resolver", got.DNS.Servers)
	}
}

// WriteConfig must materialise the embedded rule sets in rule mode, and must
// not bother in the modes that reference none.
func TestWriteConfigExtractsRuleSetsOnlyInRuleMode(t *testing.T) {
	for _, tc := range []struct {
		mode     config.ProxyMode
		wantSets bool
	}{
		{config.ModeRule, true},
		{config.ModeGlobal, false},
		{config.ModeDirect, false},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			dir := t.TempDir()
			rc := RuntimeConfig{Config: *config.Default(), Server: testVLESS(), RunDir: dir, BinaryPath: "/bin/sh"}
			rc.Config.Proxy.Mode = tc.mode
			eng, err := New(rc)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := eng.WriteConfig(rc); err != nil {
				t.Fatalf("WriteConfig: %v", err)
			}
			rulesDir := filepath.Join(dir, rules.RulesSubdir)
			entries, err := os.ReadDir(rulesDir)
			if tc.wantSets {
				if err != nil {
					t.Fatalf("rule mode did not create %s: %v", rulesDir, err)
				}
				if len(entries) != len(rules.RuleSets()) {
					t.Errorf("extracted %d rule sets, want %d", len(entries), len(rules.RuleSets()))
				}
			} else if err == nil && len(entries) > 0 {
				t.Errorf("%s mode extracted %d rule sets, want none", tc.mode, len(entries))
			}
		})
	}
}
