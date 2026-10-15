package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
)

const (
	testUUID = "11111111-1111-1111-1111-111111111111"
	testPW   = "22222222-3333-4444-5555-666666666666"
	testHost = "203.0.113.10"
)

func TestNewIsNotImplementedYet(t *testing.T) {
	eng, err := New(RuntimeConfig{RunDir: "/tmp/vender"})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("New() error = %v, want ErrNotImplemented", err)
	}
	if eng != nil {
		t.Errorf("New() engine = %v, want nil until M1", eng)
	}
	if !strings.Contains(err.Error(), "(M1)") {
		t.Errorf("error = %q, want it to name the milestone", err)
	}
}

func TestRuntimeConfigValidate(t *testing.T) {
	vless := config.Server{
		Name: "n", Type: config.TypeVLESS, Address: testHost, Port: 443, UUID: testUUID,
		Security: config.SecurityReality, SNI: "www.example.com", PublicKey: "DUMMY-PBK",
	}

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
}

type fakeEngine struct {
	cfg RuntimeConfig
}

func (f *fakeEngine) WriteConfig(rc RuntimeConfig) error { f.cfg = rc; return nil }
func (f *fakeEngine) Start() error                       { return nil }
func (f *fakeEngine) Stop() error                        { return nil }
func (f *fakeEngine) Logs() <-chan string                { return make(chan string) }
func (f *fakeEngine) Healthy() bool                      { return true }
