package core

import (
	"encoding/json"
	"testing"

	"github.com/ll-lite/vender/internal/config"
)

func TestInterfaceBindingIncludesDirectDNSButNotProxiedDNS(t *testing.T) {
	for _, mode := range []config.ProxyMode{config.ModeRule, config.ModeGlobal} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := *config.Default()
			cfg.Proxy.Mode = mode
			cfg.Engine.BindInterface = "test-interface"
			rc := RuntimeConfig{Config: cfg, Server: checkTestServer(), RunDir: t.TempDir()}
			data, err := BuildConfig(rc)
			if err != nil {
				t.Fatal(err)
			}
			var generated singboxConfig
			if err := json.Unmarshal(data, &generated); err != nil {
				t.Fatal(err)
			}
			for _, dns := range generated.DNS.Servers {
				want := ""
				if dns.Type != "fakeip" && dns.Type != "local" && dns.Detour == "" {
					want = cfg.Engine.BindInterface
				}
				if dns.BindInterface != want {
					t.Errorf("DNS %s bind_interface=%q, want %q", dns.Tag, dns.BindInterface, want)
				}
			}
		})
	}
}
