package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

const (
	testUUID = "11111111-1111-1111-1111-111111111111"
	testPW   = "22222222-3333-4444-5555-666666666666"
	testHost = "203.0.113.10"
)

// testVLESS is a complete REALITY server entry used by several tests.
func testVLESS() *config.Server {
	return &config.Server{
		Name: "Test-VLESS", Type: config.TypeVLESS, Address: testHost, Port: 443,
		UUID: testUUID, Flow: "xtls-rprx-vision", Security: config.SecurityReality,
		SNI: "www.example.com", Fingerprint: "chrome",
		PublicKey: "DUMMY-PUBLIC-KEY", ShortID: "0123456789abcdef",
		Network: "tcp", HeaderType: "none",
	}
}

func TestNewReturnsAUsableEngine(t *testing.T) {
	eng, err := New(RuntimeConfig{
		Config: *config.Default(), Server: testVLESS(),
		BinaryPath: "/bin/sh", RunDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if eng == nil {
		t.Fatal("New() = nil engine")
	}
	if _, ok := eng.(*engine); !ok {
		t.Errorf("New() = %T, want *core.engine", eng)
	}
	if eng.Healthy() {
		t.Error("Healthy() = true before Start")
	}
	if err := eng.Stop(); err != nil {
		t.Errorf("Stop() on a never-started engine = %v, want nil", err)
	}
}

func TestNewRejectsBadRuntimeConfig(t *testing.T) {
	tests := []struct {
		name    string
		rc      RuntimeConfig
		wantSub string
	}{
		{
			name:    "no run dir",
			rc:      RuntimeConfig{Config: *config.Default(), BinaryPath: "/bin/sh"},
			wantSub: "RunDir",
		},
		{
			name: "no binary",
			rc: RuntimeConfig{
				Config: *config.Default(), Server: testVLESS(), RunDir: "/tmp/vender",
			},
			wantSub: "BinaryPath",
		},
		{
			name: "proxy mode without a server",
			rc: RuntimeConfig{
				Config: *config.Default(), BinaryPath: "/bin/sh", RunDir: "/tmp/vender",
			},
			wantSub: "no server selected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.rc)
			if err == nil {
				t.Fatal("New() = nil error, want a failure")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestRuntimeConfigValidate(t *testing.T) {
	vless := *testVLESS()

	tests := []struct {
		name    string
		rc      RuntimeConfig
		wantSub string
	}{
		{
			name:    "no run dir",
			rc:      RuntimeConfig{Config: *config.Default()},
			wantSub: "RunDir",
		},
		{
			name: "global mode without a server",
			rc: RuntimeConfig{
				Config: func() config.Config { c := *config.Default(); c.Proxy.Mode = config.ModeGlobal; return c }(),
				RunDir: "/tmp/vender",
			},
			wantSub: "no server selected",
		},
		{
			name: "direct mode without a server is fine",
			rc: RuntimeConfig{
				Config: func() config.Config { c := *config.Default(); c.Proxy.Mode = config.ModeDirect; return c }(),
				RunDir: "/tmp/vender",
			},
		},
		{
			name: "rule mode with a valid server",
			rc:   RuntimeConfig{Config: *config.Default(), Server: &vless, RunDir: "/tmp/vender"},
		},
		{
			name: "invalid server",
			rc: RuntimeConfig{
				Config: *config.Default(), RunDir: "/tmp/vender",
				Server: &config.Server{Name: "n", Type: config.TypeVLESS, Address: testHost, Port: 443},
			},
			wantSub: "uuid is required",
		},
		{
			name: "invalid config",
			rc: RuntimeConfig{
				Config: config.Config{LogLevel: "loud", Proxy: config.ProxyConfig{Listen: "127.0.0.1:7890", Mode: config.ModeRule}},
				RunDir: "/tmp/vender",
			},
			wantSub: "log_level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rc.Validate()
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
		})
	}
}

// The interface itself must keep its documented shape: M1 implements it and M3
// consumes it, so a signature change here is a breaking change.
func TestEngineInterfaceShape(t *testing.T) {
	var _ Engine = (*fakeEngine)(nil)
	var _ Engine = (*engine)(nil)
}

// TestEngineExposesPid pins the one method M1 added on top of the frozen
// interface, so renaming it is a deliberate change rather than an accident.
func TestEngineExposesPid(t *testing.T) {
	eng, err := New(RuntimeConfig{
		Config: *config.Default(), Server: testVLESS(),
		BinaryPath: "/bin/sh", RunDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pidder, ok := eng.(interface{ Pid() int })
	if !ok {
		t.Fatalf("%T does not expose Pid()", eng)
	}
	if got := pidder.Pid(); got != 0 {
		t.Errorf("Pid() = %d before Start, want 0", got)
	}
}

type fakeEngine struct {
	cfg RuntimeConfig
}

func (f *fakeEngine) WriteConfig(rc RuntimeConfig) error { f.cfg = rc; return nil }
func (f *fakeEngine) Start() error                       { return nil }
func (f *fakeEngine) Stop() error                        { return nil }
func (f *fakeEngine) Logs() <-chan string                { return make(chan string) }
func (f *fakeEngine) Healthy() bool                      { return true }

// The generated config must stay within the top-level sections vender knows
// how to produce; anything else means a renderer change slipped through.
func TestBuildConfigTopLevelKeys(t *testing.T) {
	ruleMode, err := BuildConfig(RuntimeConfig{Config: *config.Default(), Server: testVLESS(), RunDir: "/tmp"})
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(ruleMode, &raw); err != nil {
		t.Fatalf("generated config is not JSON: %v", err)
	}
	want := []string{"log", "dns", "inbounds", "outbounds", "route"}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Errorf("rule-mode config has no %q key", k)
		}
	}
	if len(raw) != len(want) {
		t.Errorf("rule-mode config has %d top-level keys, want %d", len(raw), len(want))
	}

	// Direct mode has no proxy to resolve DNS through, so it emits no `dns`
	// section at all and lets sing-box use the system resolver.
	directCfg := *config.Default()
	directCfg.Proxy.Mode = config.ModeDirect
	directData, err := BuildConfig(RuntimeConfig{Config: directCfg, RunDir: "/tmp"})
	if err != nil {
		t.Fatalf("BuildConfig(direct): %v", err)
	}
	raw = nil
	if err := json.Unmarshal(directData, &raw); err != nil {
		t.Fatalf("direct config is not JSON: %v", err)
	}
	if _, ok := raw["dns"]; ok {
		t.Error("direct mode emitted a dns section")
	}
	if len(raw) != 4 {
		t.Errorf("direct-mode config has %d top-level keys, want 4", len(raw))
	}
}

func TestSplitListenRejectsGarbage(t *testing.T) {
	if _, _, err := splitListen("127.0.0.1"); err == nil {
		t.Error("splitListen(\"127.0.0.1\") = nil error, want a failure")
	}
	if _, _, err := splitListen("127.0.0.1:0"); err == nil {
		t.Error("splitListen with port 0 = nil error, want a failure")
	}
	host, port, err := splitListen("127.0.0.1:7897")
	if err != nil || host != "127.0.0.1" || port != 7897 {
		t.Errorf("splitListen = (%q, %d, %v), want (127.0.0.1, 7897, nil)", host, port, err)
	}
}

// buildRules must emit the actions before any destination rule: sniffing is
// what turns an IP-addressed connection back into a domain, and hijack-dns
// only fires on a protocol sniffing has already identified.
func TestBuildRulesActionsComeFirst(t *testing.T) {
	got, err := buildRules()
	if err != nil {
		t.Fatalf("buildRules: %v", err)
	}
	if len(got) < 3 {
		t.Fatalf("buildRules() = %+v, want actions plus destination rules", got)
	}
	if got[0].Action != string(rules.ActionSniff) {
		t.Errorf("first entry = %+v, want the sniff action", got[0])
	}
	if got[1].Action != string(rules.ActionHijackDNS) || len(got[1].Protocol) != 1 || got[1].Protocol[0] != "dns" {
		t.Errorf("second entry = %+v, want hijack-dns for the sniffed dns protocol", got[1])
	}
	// The destination rules follow, in the order the policy declares them, and
	// every one of them names an outbound.
	wantOrder := make([]string, 0, len(got)-2)
	for _, r := range rules.BuiltinRules() {
		if r.DNSOnly {
			continue
		}
		tag, err := outboundTag(r.Outbound)
		if err != nil {
			t.Fatalf("outboundTag(%q): %v", r.Outbound, err)
		}
		wantOrder = append(wantOrder, tag)
	}
	if len(got)-2 != len(wantOrder) {
		t.Fatalf("buildRules() has %d destination rules, want %d", len(got)-2, len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i+2].Outbound != want {
			t.Errorf("destination rule %d routes to %q, want %q", i, got[i+2].Outbound, want)
		}
		if got[i+2].Action != "" {
			t.Errorf("destination rule %d carries action %q and an outbound", i, got[i+2].Action)
		}
	}
	// The private-destination rule must be the first destination rule.
	if !got[2].IPIsPrivate || got[2].Outbound != tagDirect {
		t.Errorf("first destination rule = %+v, want ip_is_private -> direct", got[2])
	}
}

func TestOutboundTag(t *testing.T) {
	cases := []struct {
		in   rules.Outbound
		want string
	}{
		{rules.OutProxy, tagProxy},
		{rules.OutDirect, tagDirect},
		{rules.OutBlock, tagBlock},
	}
	for _, c := range cases {
		got, err := outboundTag(c.in)
		if err != nil {
			t.Fatalf("outboundTag(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("outboundTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := outboundTag("nonsense"); err == nil {
		t.Error("outboundTag(\"nonsense\") = nil error, want a failure")
	}
}

func TestWaitClosed(t *testing.T) {
	ch := make(chan struct{})
	if waitClosed(ch, 5*time.Millisecond) {
		t.Error("waitClosed on an open channel = true")
	}
	close(ch)
	if !waitClosed(ch, 5*time.Millisecond) {
		t.Error("waitClosed on a closed channel = false")
	}
}
