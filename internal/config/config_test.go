package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// documentedConfigYAML is exactly what SaveConfig(Default()) must produce: the
// file a fresh install has on disk, and the file users read when they want to
// know what is configurable.
const documentedConfigYAML = `log_level: info
proxy:
    listen: 127.0.0.1:7897
    mode: rule
engine:
    binary: ""
    bind_interface: ""
    tun:
        enabled: false
        stack: mixed
        mtu: 0
        auto_route: true
        strict_route: false
        interface_name: vender0
selected_server: ""
`

func TestDefaultConfigMatchesDocumentedYAML(t *testing.T) {
	got, err := yaml.Marshal(Default())
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if string(got) != documentedConfigYAML {
		t.Errorf("default config.yaml mismatch\n--- got ---\n%s\n--- want ---\n%s", got, documentedConfigYAML)
	}
}

func TestConfigNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "zero value becomes the defaults",
			in:   Config{},
			want: *Default(),
		},
		{
			name: "partial config keeps what the user set",
			in:   Config{LogLevel: "DEBUG", Proxy: ProxyConfig{Listen: "127.0.0.1:1080"}},
			want: Config{
				LogLevel: "debug",
				Proxy:    ProxyConfig{Listen: "127.0.0.1:1080", Mode: ModeRule},
				Engine:   EngineConfig{Tun: DefaultTun()},
			},
		},
		{
			name: "whitespace and case are canonicalised",
			in:   Config{LogLevel: " Info ", Proxy: ProxyConfig{Listen: " 127.0.0.1:7890 ", Mode: " GLOBAL "}},
			want: Config{
				LogLevel: "info",
				Proxy:    ProxyConfig{Listen: "127.0.0.1:7890", Mode: ModeGlobal},
				Engine:   EngineConfig{Tun: DefaultTun()},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize() = %#v, want %#v", got, tt.want)
			}
			// Idempotent.
			again := got
			again.Normalize()
			if !reflect.DeepEqual(again, got) {
				t.Errorf("Normalize() is not idempotent: %#v then %#v", got, again)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{name: "defaults are valid"},
		{name: "unknown log level", mutate: func(c *Config) { c.LogLevel = "verbose" }, wantSub: "log_level"},
		{name: "unknown mode", mutate: func(c *Config) { c.Proxy.Mode = "auto" }, wantSub: "proxy.mode"},
		{name: "empty listen", mutate: func(c *Config) { c.Proxy.Listen = "" }, wantSub: "must not be empty"},
		{name: "listen without port", mutate: func(c *Config) { c.Proxy.Listen = "127.0.0.1" }, wantSub: "host:port"},
		{name: "wildcard listen is refused", mutate: func(c *Config) { c.Proxy.Listen = ":7890" }, wantSub: "must not be empty"},
		{name: "port out of range", mutate: func(c *Config) { c.Proxy.Listen = "127.0.0.1:99999" }, wantSub: "out of range"},
		{name: "non numeric port", mutate: func(c *Config) { c.Proxy.Listen = "127.0.0.1:http" }, wantSub: "invalid port"},
		{name: "selected server is not checked here", mutate: func(c *Config) { c.SelectedServer = "ghost" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Normalize()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			err := cfg.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("error type = %T, want *ValidationError", err)
			}
		})
	}
}

func TestConfigValidateReportsEveryProblem(t *testing.T) {
	cfg := &Config{LogLevel: "nope", Proxy: ProxyConfig{Listen: "bad", Mode: "nope"}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if len(ve.Problems) != 3 {
		t.Errorf("len(Problems) = %d, want 3 (%v)", len(ve.Problems), ve.Problems)
	}
}

func TestConfigValidateAgainst(t *testing.T) {
	ss := NewServerSet()
	srv := Server{
		Name: "node-a", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW,
	}
	if err := ss.Add(srv, false); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cfg := Default()
	cfg.SelectedServer = "node-a"
	if err := cfg.ValidateAgainst(ss); err != nil {
		t.Errorf("ValidateAgainst() = %v, want nil for an existing server", err)
	}

	cfg.SelectedServer = "ghost"
	err := cfg.ValidateAgainst(ss)
	if err == nil {
		t.Fatal("ValidateAgainst() = nil, want an error for a missing server")
	}
	if !strings.Contains(err.Error(), "selected_server") {
		t.Errorf("error = %q, want it to mention selected_server", err)
	}
}

// The "auto" sentinel is accepted with at least one server, and rejected with
// none; Selection resolves it to the whole list.
func TestConfigSelectionAuto(t *testing.T) {
	ss := NewServerSet()
	for _, name := range []string{"node-a", "node-b"} {
		if err := ss.Add(Server{Name: name, Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}, false); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	single := NewServerSet()
	if err := single.Add(Server{Name: "only", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}, false); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cfg := Default()
	cfg.SelectedServer = SelectedServerAuto
	if !cfg.IsAutoServer() {
		t.Error("IsAutoServer() = false for the auto sentinel")
	}
	if err := cfg.ValidateAgainst(ss); err != nil {
		t.Errorf("ValidateAgainst(auto) = %v, want nil", err)
	}
	srv, all, err := cfg.Selection(ss)
	if err != nil {
		t.Fatalf("Selection(auto): %v", err)
	}
	if srv != nil || len(all) != 2 {
		t.Errorf("Selection(auto) = (%v, %d servers), want (nil, 2)", srv, len(all))
	}

	// Auto with no servers is a mistake worth reporting.
	if err := cfg.ValidateAgainst(NewServerSet()); err == nil {
		t.Error("ValidateAgainst(auto) with no servers = nil, want an error")
	}
	if _, _, err := cfg.Selection(NewServerSet()); err == nil {
		t.Error("Selection(auto) with no servers = nil error")
	}
	// The single case still resolves by name.
	cfg.SelectedServer = "only"
	srv, all, err = cfg.Selection(single)
	if err != nil || srv == nil || srv.Name != "only" || all != nil {
		t.Errorf("Selection(only) = (%v, %v, %v)", srv, all, err)
	}
	// Direct mode uses no server at all, even auto.
	cfg.Proxy.Mode = ModeDirect
	if srv, all, err := cfg.Selection(ss); err != nil || srv != nil || all != nil {
		t.Errorf("Selection(direct) = (%v, %v, %v), want all nil", srv, all, err)
	}
}

func TestSaveConfigRejectsInvalid(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	bad := Default()
	bad.Proxy.Mode = "sideways"
	if err := store.SaveConfig(bad); err == nil {
		t.Fatal("SaveConfig() = nil, want a validation error")
	}
	if _, err := os.Stat(store.ConfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("invalid config was written to disk: stat = %v", err)
	}
}

func TestStoreConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	// A missing config.yaml is not an error: the defaults are used.
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig on an empty dir: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("LoadConfig() = %#v, want the defaults", cfg)
	}

	cfg.LogLevel = "debug"
	cfg.Proxy.Mode = ModeGlobal
	cfg.Proxy.Listen = "127.0.0.1:1080"
	cfg.Engine.Binary = "/usr/local/bin/sing-box"
	cfg.SelectedServer = "node-a"
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	first, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	loaded, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Errorf("round trip mismatch\n got: %#v\nwant: %#v", loaded, cfg)
	}

	if err := store.SaveConfig(loaded); err != nil {
		t.Fatalf("SaveConfig (second): %v", err)
	}
	second, err := os.ReadFile(store.ConfigPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("config.yaml is not byte stable across a save/load/save cycle\n first:\n%s\nsecond:\n%s", first, second)
	}

	info, err := os.Stat(store.ConfigPath())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != plainFileMode {
		t.Errorf("config.yaml mode = %o, want %o", perm, plainFileMode)
	}
}

func TestLoadConfigToleratesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	body := "log_level: warn\nfuture_option: true\nproxy:\n    listen: 127.0.0.1:7890\n    mode: direct\n"
	if err := os.WriteFile(store.ConfigPath(), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.LogLevel != LogLevelWarn || cfg.Proxy.Mode != ModeDirect {
		t.Errorf("LoadConfig() = %#v, want warn/direct", cfg)
	}
}

func TestLoadConfigReportsBrokenYAML(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(store.ConfigPath(), []byte("proxy: [this is not a map\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := store.LoadConfig(); err == nil {
		t.Fatal("LoadConfig() = nil, want a parse error")
	}
}

func TestDefaultDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("HOME", home)

	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if filepath.Base(dir) != AppDirName {
		t.Errorf("DefaultDir() = %q, want it to end in %q", dir, AppDirName)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("DefaultDir() = %q, want an absolute path", dir)
	}
	// DefaultDir must not create anything.
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("DefaultDir() created %q (stat = %v)", dir, err)
	}
}

func TestStoreTracksDir(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if store.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", store.Dir(), dir)
	}
	if store.ConfigPath() != filepath.Join(dir, ConfigFileName) {
		t.Errorf("ConfigPath() = %q", store.ConfigPath())
	}
	if store.ServersPath() != filepath.Join(dir, ServersFileName) {
		t.Errorf("ServersPath() = %q", store.ServersPath())
	}
}
