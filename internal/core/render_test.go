package core

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
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
// engine refuse to start on the pinned version.
func TestBuildConfigCarriesNoLegacyFields(t *testing.T) {
	for _, tc := range goldenCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			data, err := BuildConfig(tc.rc)
			if err != nil {
				t.Fatalf("BuildConfig: %v", err)
			}
			if strings.Contains(string(data), "\"sniff\"") {
				t.Error("generated config contains the removed inbound \"sniff\" option")
			}
			if strings.Contains(string(data), "\"enable\"") {
				t.Error("generated config contains the legacy \"enable\" option")
			}
			var cfg struct {
				Inbounds []map[string]json.RawMessage `json:"inbounds"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			allowed := map[string]bool{"type": true, "tag": true, "listen": true, "listen_port": true}
			for _, in := range cfg.Inbounds {
				for k := range in {
					if !allowed[k] {
						t.Errorf("inbound has unexpected key %q (legacy inbounds fields are rejected by sing-box 1.13+)", k)
					}
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
