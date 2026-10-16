package core

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
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
		{
			// M3: the daemon turns on the Clash API so the TUI can read live
			// traffic and measure latency; the experimental section is the only
			// difference from the plain case, so it gets its own golden file.
			name: "clash-api",
			file: "clash_api.golden",
			rc: RuntimeConfig{
				Config:      *config.Default(),
				Server:      reality,
				RunDir:      "/run/vender",
				ClashListen: "127.0.0.1:19090",
				ClashSecret: "0123456789abcdef0123456789abcdef",
			},
		},

		// M6: the optional TUN inbound, one case per mode (the mode decides the
		// rules, the DNS section and the final outbound, and TUN has to be
		// correct in all three), plus one case that exercises every knob.
		{
			name: "tun-rule",
			file: "tun_rule.golden",
			rc: RuntimeConfig{
				Config: func() config.Config {
					c := *config.Default()
					c.Engine.Tun.Enabled = true
					return c
				}(),
				Server: reality, RunDir: "/run/vender",
			},
		},
		{
			name: "tun-global",
			file: "tun_global.golden",
			rc: RuntimeConfig{
				Config: func() config.Config {
					c := *config.Default()
					c.Proxy.Mode = config.ModeGlobal
					c.Engine.Tun.Enabled = true
					// A rule-set exclusion in a mode that otherwise declares
					// no rule sets at all: the set still has to be declared and
					// its file extracted, or `sing-box check` refuses the
					// config.
					c.Engine.Tun.RouteExcludeAddressSet = []string{"geoip-cn", "geosite-cn"}
					return c
				}(),
				Server: hy2, RunDir: "/run/vender",
			},
		},
		{
			name: "tun-direct",
			file: "tun_direct.golden",
			rc: RuntimeConfig{
				Config: func() config.Config {
					c := *config.Default()
					c.Proxy.Mode = config.ModeDirect
					c.Engine.Tun.Enabled = true
					return c
				}(),
				RunDir: "/run/vender",
			},
		},
		{
			// Every knob at once: a different stack, an explicit MTU and
			// address, strict_route, a CIDR exclusion, a rule-set exclusion
			// (which must also be declared in route.rule_set even though rule
			// mode already declares it), and bind_interface, which suppresses
			// auto_detect_interface because the outbound is already pinned.
			name: "tun-exclusions",
			file: "tun_exclusions.golden",
			rc: RuntimeConfig{
				Config: func() config.Config {
					c := *config.Default()
					c.Engine.BindInterface = "wlp0s20f3"
					c.Engine.Tun = config.TunConfig{
						Enabled:                true,
						Stack:                  config.TunStackSystem,
						MTU:                    1500,
						AutoRoute:              config.Bool(true),
						StrictRoute:            true,
						InterfaceName:          "vetun9",
						Address:                []string{"172.20.0.1/30"},
						RouteExcludeAddress:    []string{"192.168.0.0/16", "198.18.0.0/15"},
						RouteExcludeAddressSet: []string{"geoip-cn"},
					}
					return c
				}(),
				Server: reality, RunDir: "/run/vender",
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
			// The TUN inbound's own keys. They are legitimate only on a tun
			// inbound: on a mixed one they would be the legacy inbound options
			// sing-box 1.13 removed.
			tunOnly := map[string]bool{
				"interface_name": true, "address": true, "mtu": true, "stack": true,
				"auto_route": true, "strict_route": true,
				"route_exclude_address": true, "route_exclude_address_set": true,
				// Removed in sing-box 1.12; vender must use `address`.
				"inet4_address": true, "inet6_address": true, "inet4_route_address": true,
			}
			for _, in := range inbounds {
				var typ string
				if err := json.Unmarshal(in["type"], &typ); err != nil {
					t.Fatalf("unmarshal inbound type: %v", err)
				}
				for k := range in {
					if allowed[k] {
						continue
					}
					if tunOnly[k] {
						if typ != "tun" {
							t.Errorf("%s inbound has TUN key %q", typ, k)
						}
						if k == "inet4_address" || k == "inet6_address" || k == "inet4_route_address" {
							t.Errorf("inbound uses %q, which sing-box 1.12 removed; use `address`", k)
						}
						continue
					}
					t.Errorf("inbound has unexpected key %q (legacy inbounds fields are rejected by sing-box 1.13+)", k)
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

// --- M6: TUN mode -----------------------------------------------------------

// tunConfig builds a rule-mode config with TUN on, so a test can vary one knob.
func tunConfig(mutate ...func(*config.Config)) config.Config {
	c := *config.Default()
	c.Engine.Tun.Enabled = true
	for _, m := range mutate {
		m(&c)
	}
	return c
}

// decodeTun returns the rendered tun inbound, or nil when there is none.
func decodeTun(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var cfg struct {
		Inbounds []map[string]json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, in := range cfg.Inbounds {
		var typ string
		_ = json.Unmarshal(in["type"], &typ)
		if typ == "tun" {
			return in
		}
	}
	return nil
}

func decodeRoute(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var cfg struct {
		Route map[string]json.RawMessage `json:"route"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return cfg.Route
}

// TUN off must leave the generated document exactly as M1/M2 wrote it: the
// mixed inbound only, no auto_detect_interface, no tun device. That is what
// keeps every non-TUN user's config byte-identical after an upgrade.
func TestTunOffEmitsNothingTunShaped(t *testing.T) {
	for _, mode := range config.ProxyModes() {
		t.Run(string(mode), func(t *testing.T) {
			cfg := *config.Default()
			cfg.Proxy.Mode = mode
			rc := RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"}
			if mode == config.ModeDirect {
				rc.Server = nil
			}
			data, err := BuildConfig(rc)
			if err != nil {
				t.Fatalf("BuildConfig: %v", err)
			}
			if in := decodeTun(t, data); in != nil {
				t.Errorf("TUN off produced a tun inbound: %v", in)
			}
			if route := decodeRoute(t, data); len(route["auto_detect_interface"]) != 0 {
				t.Errorf("TUN off emitted auto_detect_interface")
			}
			if strings.Contains(string(data), "vender0") {
				t.Errorf("TUN off mentions the tun device name:\n%s", data)
			}
		})
	}
}

// Every knob has to reach sing-box, and auto_route: false has to survive as an
// explicit false rather than being filled in with the default.
func TestTunKnobsAreRendered(t *testing.T) {
	cfg := tunConfig(func(c *config.Config) {
		c.Engine.Tun = config.TunConfig{
			Enabled:                true,
			Stack:                  config.TunStackGvisor,
			MTU:                    1420,
			AutoRoute:              config.Bool(false),
			StrictRoute:            true,
			InterfaceName:          "vetun1",
			Address:                []string{"10.9.0.1/30"},
			RouteExcludeAddress:    []string{"192.168.0.0/16"},
			RouteExcludeAddressSet: []string{"geoip-cn"},
		}
	})
	data, err := BuildConfig(RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var in struct {
		InterfaceName          string   `json:"interface_name"`
		Address                []string `json:"address"`
		MTU                    int      `json:"mtu"`
		Stack                  string   `json:"stack"`
		AutoRoute              *bool    `json:"auto_route"`
		StrictRoute            bool     `json:"strict_route"`
		RouteExcludeAddress    []string `json:"route_exclude_address"`
		RouteExcludeAddressSet []string `json:"route_exclude_address_set"`
	}
	if err := json.Unmarshal(mustJSON(t, decodeTun(t, data)), &in); err != nil {
		t.Fatalf("decode tun inbound: %v", err)
	}
	if in.InterfaceName != "vetun1" || in.Stack != config.TunStackGvisor || in.MTU != 1420 || !in.StrictRoute {
		t.Errorf("tun inbound = %+v", in)
	}
	// auto_route: false is not emitted at all: false is sing-box's own default,
	// so the key would be noise, and its absence means exactly what the user
	// asked for.
	if _, present := decodeTun(t, data)["auto_route"]; present {
		t.Errorf("auto_route: false was rendered as an explicit field")
	}
	if len(in.Address) != 1 || in.Address[0] != "10.9.0.1/30" {
		t.Errorf("address = %v", in.Address)
	}
	if len(in.RouteExcludeAddress) != 1 || len(in.RouteExcludeAddressSet) != 1 {
		t.Errorf("exclusions = %v / %v", in.RouteExcludeAddress, in.RouteExcludeAddressSet)
	}
	// auto_route: false still means the engine is present; the mixed inbound
	// must be untouched.
	if len(data) == 0 {
		t.Fatal("empty config")
	}
}

func mustJSON(t *testing.T, m map[string]json.RawMessage) []byte {
	t.Helper()
	out := make([]byte, 0, 128)
	out = append(out, '{')
	first := true
	for k, v := range m {
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, '"')
		out = append(out, k...)
		out = append(out, '"', ':')
		out = append(out, v...)
	}
	return append(out, '}')
}

// A TUN exclusion set must be declared exactly once, even when the built-in
// policy already declares it; and it must be declared in the modes that
// otherwise declare no rule sets at all.
func TestTunExclusionSetsAreDeclaredOnce(t *testing.T) {
	for _, mode := range config.ProxyModes() {
		t.Run(string(mode), func(t *testing.T) {
			cfg := tunConfig(func(c *config.Config) {
				c.Proxy.Mode = mode
				c.Engine.Tun.RouteExcludeAddressSet = []string{"geoip-cn", "geosite-cn"}
			})
			rc := RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"}
			if mode == config.ModeDirect {
				rc.Server = nil
			}
			data, err := BuildConfig(rc)
			if err != nil {
				t.Fatalf("BuildConfig: %v", err)
			}
			var route struct {
				RuleSet []struct {
					Tag string `json:"tag"`
				} `json:"rule_set"`
			}
			if err := json.Unmarshal(mustJSON(t, decodeRoute(t, data)), &route); err != nil {
				t.Fatalf("unmarshal route: %v", err)
			}
			counts := map[string]int{}
			for _, rs := range route.RuleSet {
				counts[rs.Tag]++
			}
			for _, tag := range []string{"geoip-cn", "geosite-cn"} {
				if counts[tag] != 1 {
					t.Errorf("rule set %q declared %d times, want 1", tag, counts[tag])
				}
			}
			if mode == config.ModeRule {
				if len(route.RuleSet) != len(rules.ReferencedRuleSets()) {
					t.Errorf("rule mode declared %d sets, want the policy's %d",
						len(route.RuleSet), len(rules.ReferencedRuleSets()))
				}
			} else if len(route.RuleSet) != 2 {
				t.Errorf("%s mode declared %d sets, want only the 2 exclusions", mode, len(route.RuleSet))
			}
		})
	}
}

// WriteConfig must extract the rule sets whenever the config declares any, so a
// TUN exclusion in global or direct mode still has files to point at.
func TestWriteConfigExtractsRuleSetsForTunExclusions(t *testing.T) {
	dir := t.TempDir()
	cfg := tunConfig(func(c *config.Config) {
		c.Proxy.Mode = config.ModeDirect
		c.Engine.Tun.RouteExcludeAddressSet = []string{"geoip-cn"}
	})
	rc := RuntimeConfig{Config: cfg, RunDir: dir, BinaryPath: "/bin/sh"}
	eng, err := New(rc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := eng.WriteConfig(rc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rules.RulesSubdir, "geoip-cn.srs")); err != nil {
		t.Errorf("direct mode + TUN exclusion did not extract geoip-cn.srs: %v", err)
	}
}

// A global-mode TUN keeps no destination rules but does get the two actions:
// without hijack-dns, DNS would leave the host as raw packets to port 53 and
// "everything through the proxy" would be a lie for the one protocol every
// connection starts with.
func TestTunGlobalKeepsOnlyTheActions(t *testing.T) {
	cfg := tunConfig(func(c *config.Config) { c.Proxy.Mode = config.ModeGlobal })
	data, err := BuildConfig(RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var route struct {
		Rules []struct {
			Action   string   `json:"action"`
			Protocol []string `json:"protocol"`
			Outbound string   `json:"outbound"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(mustJSON(t, decodeRoute(t, data)), &route); err != nil {
		t.Fatalf("decode route: %v", err)
	}
	if len(route.Rules) != 2 {
		t.Fatalf("global+TUN emitted %d rules, want the 2 actions", len(route.Rules))
	}
	for _, r := range route.Rules {
		if r.Action == "" || r.Outbound != "" {
			t.Errorf("global+TUN emitted a routing rule: %+v", r)
		}
	}
	if route.Rules[1].Action != "hijack-dns" {
		t.Errorf("second rule = %+v, want hijack-dns", route.Rules[1])
	}
}

// Direct mode must not proxy, TUN or not: no proxy outbound, no actions, no DNS
// section of its own.
func TestTunDirectModeDoesNotProxy(t *testing.T) {
	cfg := tunConfig(func(c *config.Config) { c.Proxy.Mode = config.ModeDirect })
	data, err := BuildConfig(RuntimeConfig{Config: cfg, RunDir: "/run/vender"})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var got struct {
		Inbounds  []map[string]json.RawMessage `json:"inbounds"`
		Outbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"outbounds"`
		Route struct {
			Final string            `json:"final"`
			Rules []json.RawMessage `json:"rules"`
		} `json:"route"`
		DNS json.RawMessage `json:"dns"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Inbounds) != 2 {
		t.Errorf("direct+TUN has %d inbounds, want mixed and tun", len(got.Inbounds))
	}
	if got.Route.Final != tagDirect || len(got.Route.Rules) != 0 {
		t.Errorf("direct+TUN route = %s / %d rules, want direct with none", got.Route.Final, len(got.Route.Rules))
	}
	if len(got.DNS) != 0 {
		t.Errorf("direct mode emitted a dns section: %s", got.DNS)
	}
	for _, o := range got.Outbounds {
		if o.Tag == tagProxy {
			t.Errorf("direct+TUN emitted a proxy outbound")
		}
	}
}

// bind_interface already pins the outbound, so auto_detect_interface is not
// emitted on top of it.
func TestTunAutoDetectInterfaceYieldsToBindInterface(t *testing.T) {
	cfg := tunConfig(func(c *config.Config) { c.Engine.BindInterface = "wlp0s20f3" })
	data, err := BuildConfig(RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if route := decodeRoute(t, data); len(route["auto_detect_interface"]) != 0 {
		t.Errorf("bind_interface did not suppress auto_detect_interface")
	}
}

// A TUN exclusion that names a rule set vender does not embed is a
// configuration mistake, and validation must say so before sing-box is spawned.
func TestTunUnknownExclusionSetIsRejected(t *testing.T) {
	cfg := tunConfig(func(c *config.Config) {
		c.Engine.Tun.RouteExcludeAddressSet = []string{"geoip-private"}
	})
	_, err := BuildConfig(RuntimeConfig{Config: cfg, Server: testVLESS(), RunDir: "/run/vender"})
	if err == nil {
		t.Fatal("BuildConfig accepted an unknown rule set")
	}
	if !strings.Contains(err.Error(), "geoip-private") {
		t.Errorf("error = %v, want it to name the set", err)
	}
}

// The documented TUN knobs are validated before the engine is touched, and a
// disabled TUN block is inert: a typo in a feature that is off must not block
// start-up.
func TestTunValidation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tun     config.TunConfig
		enabled bool
		wantSub string
	}{
		{name: "defaults are valid", tun: config.DefaultTun(), enabled: true},
		{name: "unknown stack", enabled: true,
			tun:     config.TunConfig{Enabled: true, Stack: "bogus", AutoRoute: config.Bool(true), InterfaceName: "vender0"},
			wantSub: "stack"},
		{name: "mtu below the IPv6 minimum", enabled: true,
			tun:     config.TunConfig{Enabled: true, Stack: "mixed", MTU: 576, AutoRoute: config.Bool(true), InterfaceName: "vender0"},
			wantSub: "mtu"},
		{name: "interface name too long for Linux", enabled: true,
			tun:     config.TunConfig{Enabled: true, Stack: "mixed", AutoRoute: config.Bool(true), InterfaceName: "a-very-long-tun-name"},
			wantSub: "interface_name"},
		{name: "bad CIDR", enabled: true,
			tun: config.TunConfig{Enabled: true, Stack: "mixed", AutoRoute: config.Bool(true),
				InterfaceName: "vender0", RouteExcludeAddress: []string{"192.168.0.0"}},
			wantSub: "route_exclude_address"},
		{name: "the same typo is ignored while TUN is off", enabled: false,
			tun: config.TunConfig{Enabled: false, Stack: "bogus"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := *config.Default()
			cfg.Engine.Tun = tt.tun
			err := cfg.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}
