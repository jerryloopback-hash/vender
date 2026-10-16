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
