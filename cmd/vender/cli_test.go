package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/autostart"
	"github.com/ll-lite/vender/internal/config"
)

// Dummy values only: never commit real credentials.
const (
	testUUID = "11111111-1111-1111-1111-111111111111"
	testPBK  = "DUMMY-FAKE-PUBLIC-KEY-FOR-TESTS-ONLY-0000000000"
	testSID  = "0123456789abcdef"
	testPW   = "22222222-3333-4444-5555-666666666666"
	testHost = "203.0.113.10"

	vlessLink = "vless://" + testUUID + "@" + testHost + ":443" +
		"?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.com" +
		"&fp=chrome&pbk=" + testPBK + "&sid=" + testSID + "&type=tcp&headerType=none#Test-VLESS"
	hy2Link = "hysteria2://" + testPW + "@" + testHost + ":8443" +
		"/?sni=www.example.com&obfs=salamander&obfs-password=cafebabe&insecure=1#Test-Hy2"
)

// runCLI executes the root command with args and returns everything it wrote
// plus the error.
//
// Every invocation gets a throwaway --run-dir: the daemon and status commands
// would otherwise consult (and the daemon would write to) the real
// os.UserCacheDir()/vender of whoever runs the tests.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCLIIn(t, t.TempDir(), args...)
}

// runCLIIn is runCLI with an explicit run directory.
func runCLIIn(t *testing.T, runDir string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"--run-dir", runDir}, args...))
	err := cmd.Execute()
	return buf.String(), err
}

func TestCLIVersion(t *testing.T) {
	out, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, appName) || !strings.Contains(out, version) {
		t.Errorf("version output = %q, want it to mention %s and %s", out, appName, version)
	}
	if !strings.Contains(out, "linux") && !strings.Contains(out, "windows") && !strings.Contains(out, "darwin") {
		t.Errorf("version output = %q, want the platform line", out)
	}
}

func TestCLIVersionRejectsArgs(t *testing.T) {
	if _, err := runCLI(t, "version", "extra"); err == nil {
		t.Error("version extra = nil error, want a usage error")
	}
}

// `autostart` writes to the real start-at-login configuration, so the CLI tests
// substitute recorders for the three entry points: the command's output and exit
// codes are exercised, and the host is never touched. The package's own tests
// cover the real logic against an isolated HOME.
func TestCLIAutostart(t *testing.T) {
	restore := autostartEnable
	restoreOff := autostartDisable
	restoreStatus := autostartStatus
	restoreOpts := autostartOptions
	t.Cleanup(func() {
		autostartEnable, autostartDisable, autostartStatus = restore, restoreOff, restoreStatus
		autostartOptions = restoreOpts
	})
	// The real option reader would look at the host's config.yaml.
	autostartOptions = func(*app) autostart.Options { return autostart.Options{} }

	enabled := false
	autostartEnable = func(autostart.Options) error { enabled = true; return nil }
	autostartDisable = func() error { enabled = false; return nil }
	autostartStatus = func(autostart.Options) (autostart.State, error) {
		if enabled {
			return autostart.State{
				Enabled:   true,
				Mechanism: autostart.MechanismSystemdUser,
				Target:    "/home/test/.config/systemd/user/vender.service",
				Detail:    "systemd user unit installed",
			}, nil
		}
		return autostart.State{
			Enabled:   false,
			Mechanism: autostart.MechanismSystemdUser,
			Target:    "/home/test/.config/systemd/user/vender.service",
			Detail:    "not registered",
		}, nil
	}

	// Disabled: the output explains itself and the exit code is non-zero.
	out, err := runCLI(t, "autostart", "status")
	if err == nil {
		t.Fatalf("status when disabled = nil error, want a non-zero exit (output: %q)", out)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error = %q", err)
	}
	for _, want := range []string{"state        disabled", "mechanism    systemd-user", "target       /home/test/.config/systemd/user/vender.service"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output = %q, want %q", out, want)
		}
	}

	// on registers, prints where, and is reflected by status (exit 0 now).
	out, err = runCLI(t, "autostart", "on")
	if err != nil {
		t.Fatalf("autostart on: %v\n%s", err, out)
	}
	if !enabled {
		t.Error("autostart on did not call Enable")
	}
	if !strings.Contains(out, "enabled") {
		t.Errorf("on output = %q", out)
	}
	if out, err = runCLI(t, "autostart", "status"); err != nil {
		t.Fatalf("status when enabled: %v\n%s", err, out)
	}

	// --json is machine-readable and carries the same state.
	out, err = runCLI(t, "autostart", "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var got struct {
		Enabled   bool   `json:"enabled"`
		Mechanism string `json:"mechanism"`
		Target    string `json:"target"`
		Action    string `json:"action"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, out)
	}
	if !got.Enabled || got.Mechanism != "systemd-user" || got.Action != "status" {
		t.Errorf("json = %+v", got)
	}

	// off removes it and is idempotent at the CLI level.
	if out, err = runCLI(t, "autostart", "off"); err != nil {
		t.Fatalf("autostart off: %v\n%s", err, out)
	}
	if enabled {
		t.Error("autostart off did not call Disable")
	}
	if _, err = runCLI(t, "autostart", "off"); err != nil {
		t.Errorf("autostart off (again) = %v, want it to be idempotent", err)
	}
}

// An argument to a subcommand is a usage error, not a silent no-op.
func TestCLIAutostartRejectsArgs(t *testing.T) {
	if _, err := runCLI(t, "autostart", "on", "extra"); err == nil {
		t.Error("autostart on extra = nil error, want a usage error")
	}
}

func TestCLIRootHelpListsEveryCommand(t *testing.T) {
	out, err := runCLI(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"import", "servers", "daemon", "status", "proxy", "tui", "autostart", "version"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output does not mention %q:\n%s", want, out)
		}
	}
}

// `vender tui` is a real command from M3 on, and it must stay usable as a
// non-terminal: piping it must print a snapshot instead of failing.
func TestCLITUIHelp(t *testing.T) {
	out, err := runCLI(t, "tui", "--help")
	if err != nil {
		t.Fatalf("tui --help: %v", err)
	}
	for _, want := range []string{"Dashboard", "Nodes", "Logs", "Settings", "quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("tui help does not mention %q:\n%s", want, out)
		}
	}
	if _, err := runCLI(t, "tui", "extra"); err == nil {
		t.Error("tui with an argument = nil error, want a usage error")
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	if _, err := runCLI(t, "frobnicate"); err == nil {
		t.Error("unknown command = nil error, want a failure")
	}
}

func TestCLIImportServersListRm(t *testing.T) {
	dir := t.TempDir()

	out, err := runCLI(t, "import", vlessLink, "--config-dir", dir)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	for _, want := range []string{"Imported", "Test-VLESS", "vless", testHost + ":443", "selected as the active server"} {
		if !strings.Contains(out, want) {
			t.Errorf("import output = %q, want it to mention %q", out, want)
		}
	}

	// servers.yaml exists, holds no real secrets, and is mode 0600.
	serversPath := filepath.Join(dir, config.ServersFileName)
	info, err := os.Stat(serversPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("servers.yaml mode = %o, want 600", perm)
	}
	body, err := os.ReadFile(serversPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "servers:") {
		t.Errorf("servers.yaml = %s, want a servers key", body)
	}

	// config.yaml was created with the defaults plus the selection.
	cfg, err := config.NewStoreAt(dir).LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "Test-VLESS" {
		t.Errorf("SelectedServer = %q, want Test-VLESS", cfg.SelectedServer)
	}
	if cfg.Proxy.Listen != config.DefaultListen {
		t.Errorf("Listen = %q, want the default %q", cfg.Proxy.Listen, config.DefaultListen)
	}

	// A second import of the same link is refused...
	if _, err := runCLI(t, "import", hy2Link, "--name", "Test-VLESS", "--config-dir", dir); !errors.Is(err, config.ErrDuplicate) {
		t.Errorf("duplicate import = %v, want ErrDuplicate", err)
	}
	// ...unless --force is given.
	if out, err := runCLI(t, "import", hy2Link, "--name", "Test-VLESS", "--force", "--config-dir", dir); err != nil {
		t.Errorf("import --force: %v\n%s", err, out)
	}

	// servers list shows both entries, marking the active one.
	out, err = runCLI(t, "servers", "list", "--config-dir", dir)
	if err != nil {
		t.Fatalf("servers list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "ENDPOINT") {
		t.Errorf("servers list header missing:\n%s", out)
	}
	if !strings.Contains(out, "Test-VLESS") || !strings.Contains(out, "hysteria2") {
		t.Errorf("servers list = %q, want the imported servers", out)
	}
	if !strings.Contains(out, "*") {
		t.Errorf("servers list = %q, want the active server marked", out)
	}

	// servers list --links emits links that parse back to the same servers.
	out, err = runCLI(t, "servers", "list", "--links", "--config-dir", dir)
	if err != nil {
		t.Fatalf("servers list --links: %v\n%s", err, out)
	}
	var links []string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) == 2 && strings.Contains(parts[1], "://") {
			links = append(links, strings.TrimSpace(parts[1]))
		}
	}
	if len(links) != 1 {
		t.Fatalf("got %d links from --links, want 1:\n%s", len(links), out)
	}
	stored, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	parsed, err := config.ParseShareLink(links[0])
	if err != nil {
		t.Fatalf("ParseShareLink(%q): %v", links[0], err)
	}
	want := stored.Servers[0]
	if parsed.UUID != want.UUID || parsed.PublicKey != want.PublicKey || parsed.Name != want.Name {
		t.Errorf("link %q does not round trip: got %#v want %#v", links[0], *parsed, want)
	}

	// servers rm deletes it and clears the selection.
	out, err = runCLI(t, "servers", "rm", "test-vless", "--config-dir", dir)
	if err != nil {
		t.Fatalf("servers rm: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("servers rm output = %q", out)
	}
	if !strings.Contains(out, "no server is selected") {
		t.Errorf("servers rm output = %q, want it to mention the cleared selection", out)
	}
	after, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if after.Len() != 0 {
		t.Errorf("Len() = %d, want 0 after rm", after.Len())
	}
	cfg, err = config.NewStoreAt(dir).LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "" {
		t.Errorf("SelectedServer = %q, want it cleared", cfg.SelectedServer)
	}

	if _, err := runCLI(t, "servers", "rm", "test-vless", "--config-dir", dir); !errors.Is(err, config.ErrNotFound) {
		t.Errorf("servers rm (again) = %v, want ErrNotFound", err)
	}
}

func TestCLIImportHysteria2(t *testing.T) {
	dir := t.TempDir()
	out, err := runCLI(t, "import", hy2Link, "--config-dir", dir)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Test-Hy2") {
		t.Errorf("import output = %q", out)
	}
	ss, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	srv, ok := ss.Get("Test-Hy2")
	if !ok {
		t.Fatal("Test-Hy2 not persisted")
	}
	if srv.Type != config.TypeHysteria2 || srv.Password != testPW || !srv.Insecure || srv.Obfs != "salamander" {
		t.Errorf("stored server = %#v", *srv)
	}

	// hy2:// is an accepted alias.
	out, err = runCLI(t, "import", strings.Replace(hy2Link, "hysteria2://", "hy2://", 1), "--name", "Hy2-Alias", "--config-dir", dir)
	if err != nil {
		t.Fatalf("import hy2://: %v\n%s", err, out)
	}
	ss, err = config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if _, ok := ss.Get("Hy2-Alias"); !ok {
		t.Error("hy2:// alias did not import")
	}
}

func TestCLIImportName(t *testing.T) {
	dir := t.TempDir()
	if out, err := runCLI(t, "import", vlessLink, "--name", "custom-name", "--config-dir", dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	ss, err := config.NewStoreAt(dir).LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if _, ok := ss.Get("custom-name"); !ok {
		t.Errorf("names = %v, want custom-name", ss.Names())
	}
}

func TestCLIImportRejectsBadLinks(t *testing.T) {
	tests := []struct {
		name    string
		link    string
		wantSub string
	}{
		{name: "unsupported scheme", link: "wireguard://" + testUUID + "@" + testHost + ":443#x", wantSub: "unsupported share link scheme"},
		{name: "garbage", link: "hello", wantSub: "missing scheme"},
		{name: "bad uuid", link: "vless://nope@" + testHost + ":443#x", wantSub: "not a UUID"},
		{name: "reality without public key", link: "vless://" + testUUID + "@" + testHost + ":443?security=reality&sni=www.example.com#x", wantSub: "public_key"},
		{name: "hy2 without password", link: "hysteria2://" + testHost + ":8443/#x", wantSub: "missing password"},
		{name: "vless ws transport is out of scope", link: "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=www.example.com&type=ws#x", wantSub: "tcp only"},
		{name: "vmess with a broken payload", link: "vmess://@@@not-base64@@@", wantSub: "vmess"},
		{name: "ss with a plugin", link: "ss://YWVzLTI1Ni1nY206cGFzcw@203.0.113.10:8388/?plugin=obfs-local#x", wantSub: "plugin"},
		{name: "trojan without a password", link: "trojan://@203.0.113.10:443#x", wantSub: "missing password"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runCLI(t, "import", tt.link, "--config-dir", dir)
			if err == nil {
				t.Fatalf("import = nil error, want a failure (output: %q)", out)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
			// Nothing may be written when the link is rejected.
			if _, statErr := os.Stat(filepath.Join(dir, config.ServersFileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("servers.yaml exists after a rejected import (stat = %v)", statErr)
			}
		})
	}
}

func TestCLIImportRequiresExactlyOneArg(t *testing.T) {
	if _, err := runCLI(t, "import"); err == nil {
		t.Error("import with no args = nil error, want a usage error")
	}
	if _, err := runCLI(t, "import", vlessLink, "extra"); err == nil {
		t.Error("import with two args = nil error, want a usage error")
	}
}

func TestCLIServersListEmpty(t *testing.T) {
	dir := t.TempDir()
	out, err := runCLI(t, "servers", "list", "--config-dir", dir)
	if err != nil {
		t.Fatalf("servers list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No servers configured") {
		t.Errorf("output = %q, want a friendly empty message", out)
	}
	if !strings.Contains(out, "vender import") {
		t.Errorf("output = %q, want a hint about the import command", out)
	}
}

func TestCLIServersListReportsBrokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "servers:\n  - name: broken\n    type: hysteria2\n    server: " + testHost + "\n    port: 8443\n"
	if err := os.WriteFile(filepath.Join(dir, config.ServersFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := runCLI(t, "servers", "list", "--config-dir", dir)
	if err == nil {
		t.Fatal("servers list = nil error, want a validation error")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error = %q, want it to explain the missing password", err)
	}
}

func TestCLIImportMarksFirstServerAsSelectedOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	if out, err := runCLI(t, "import", vlessLink, "--config-dir", dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "import", hy2Link, "--config-dir", dir); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	cfg, err := config.NewStoreAt(dir).LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "Test-VLESS" {
		t.Errorf("SelectedServer = %q, want Test-VLESS (a later import must not steal the selection)", cfg.SelectedServer)
	}
}

func TestCLIVersionFlagOnRoot(t *testing.T) {
	out, err := runCLI(t, "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out, version) {
		t.Errorf("--version output = %q, want %q", out, version)
	}
}

func TestCLIImportFromFreshConfigDirInNestedPath(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "deep", "nested", "vender")
	out, err := runCLI(t, "import", vlessLink, "--config-dir", dir)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, config.ServersFileName)); err != nil {
		t.Errorf("servers.yaml was not created in %s: %v", dir, err)
	}
	if !strings.Contains(out, fmt.Sprintf("saved to %s", filepath.Join(dir, config.ServersFileName))) {
		t.Errorf("import output = %q, want it to name the file it wrote", out)
	}
}

// --- M6: `vender tun` -------------------------------------------------------

// The TUN command is a configuration edit when no daemon is running: the
// setting has to survive, and `status` has to exit non-zero while it is off
// (the same contract as `vender status` and `vender autostart status`).
func TestCLITunWithoutADaemon(t *testing.T) {
	cfgDir := t.TempDir()
	runDir := t.TempDir()
	store := config.NewStoreAt(cfgDir)

	// Off by default: status explains itself and exits non-zero.
	out, err := runCLIIn(t, runDir, "tun", "status", "--config-dir", cfgDir)
	if err == nil {
		t.Fatalf("tun status while off = nil error, want a non-zero exit (output %q)", out)
	}
	if !strings.Contains(out, "TUN mode: off") {
		t.Errorf("status output = %q", out)
	}

	// On: persisted, and reported.
	out, err = runCLIIn(t, runDir, "tun", "on", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("tun on: %v\n%s", err, out)
	}
	if !strings.Contains(out, "TUN mode enabled: on") {
		t.Errorf("tun on output = %q", out)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Engine.Tun.Enabled {
		t.Error("tun on did not persist the setting")
	}
	// The verdict is always reported, so a user is never left guessing whether
	// TUN can work here.
	if !strings.Contains(out, "privileges") {
		t.Errorf("tun on output does not report the privilege verdict: %q", out)
	}

	// Idempotent, and now status exits 0.
	if _, err := runCLIIn(t, runDir, "tun", "on", "--config-dir", cfgDir); err != nil {
		t.Fatalf("tun on twice: %v", err)
	}
	if _, err := runCLIIn(t, runDir, "tun", "status", "--config-dir", cfgDir); err != nil {
		t.Errorf("tun status while on = %v, want nil", err)
	}

	// --json is machine-readable and reports the same setting.
	out, err = runCLIIn(t, runDir, "tun", "status", "--config-dir", cfgDir, "--json")
	if err != nil {
		t.Fatalf("tun status --json: %v", err)
	}
	var got struct {
		Enabled    bool   `json:"enabled"`
		Action     string `json:"action"`
		Stack      string `json:"stack"`
		Iface      string `json:"interface"`
		AutoRoute  bool   `json:"autoRoute"`
		Privileges bool   `json:"privilegesOk"`
		Daemon     int    `json:"daemon"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("tun status --json is not JSON: %v\n%s", err, out)
	}
	if !got.Enabled || got.Stack != config.DefaultTunStack || got.Iface != config.DefaultTunInterfaceName || !got.AutoRoute {
		t.Errorf("json = %+v", got)
	}
	if got.Action != "status" {
		t.Errorf("action = %q", got.Action)
	}
	if got.Daemon != 0 {
		t.Errorf("daemon = %d, want 0 (not running)", got.Daemon)
	}

	// Off again.
	out, err = runCLIIn(t, runDir, "tun", "off", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("tun off: %v\n%s", err, out)
	}
	if cfg, err = store.LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Engine.Tun.Enabled {
		t.Error("tun off did not persist the setting")
	}
	if _, err := runCLIIn(t, runDir, "tun", "status", "--config-dir", cfgDir); err == nil {
		t.Error("tun status after off = nil error, want a non-zero exit")
	}
}

// `tun` follows the same shape as `proxy` and `autostart`: an unknown
// subcommand prints the usage (cobra's behaviour for a group command) instead
// of doing something surprising, and a stray argument to a leaf is an error.
func TestCLITunRejectsUnknownSubcommand(t *testing.T) {
	out, err := runCLI(t, "tun", "sideways")
	if err != nil {
		t.Fatalf("tun sideways: %v", err)
	}
	for _, want := range []string{"Available Commands:", "on", "off", "status"} {
		if !strings.Contains(out, want) {
			t.Errorf("tun sideways does not print the usage (%q missing):\n%s", want, out)
		}
	}
	if _, err := runCLI(t, "tun", "on", "extra"); err == nil {
		t.Error("tun on with an argument = nil error")
	}
}

// `autostart on` has to hand the registration what the daemon will need, which
// is read from the configuration the CLI was pointed at: with TUN on, the
// mechanism has to be the privileged one.
func TestCLIAutostartPassesTheTunSetting(t *testing.T) {
	restoreOpts := autostartOptions
	restoreEnable := autostartEnable
	restoreStatus := autostartStatus
	t.Cleanup(func() {
		autostartOptions, autostartEnable, autostartStatus = restoreOpts, restoreEnable, restoreStatus
	})

	cfgDir := t.TempDir()
	runDir := t.TempDir()
	store := config.NewStoreAt(cfgDir)

	var seen autostart.Options
	autostartEnable = func(o autostart.Options) error { seen = o; return nil }
	autostartStatus = func(autostart.Options) (autostart.State, error) {
		return autostart.State{Enabled: true, Mechanism: autostart.MechanismSystemdSystem, Target: "/etc/systemd/system/vender.service"}, nil
	}
	autostartOptions = defaultAutostartOptions

	// TUN off: the per-user mechanism, no capability hint.
	cfg := config.Default()
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if _, err := runCLIIn(t, runDir, "autostart", "on", "--config-dir", cfgDir); err != nil {
		t.Fatalf("autostart on: %v", err)
	}
	if seen.TUN || seen.CapabilityHint != "" {
		t.Errorf("options with TUN off = %+v", seen)
	}

	// TUN on: the option travels, and the hint explains what is missing (this
	// test process is not privileged, so there is always something).
	cfg.Engine.Tun.Enabled = true
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	out, err := runCLIIn(t, runDir, "autostart", "on", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("autostart on with TUN: %v\n%s", err, out)
	}
	if !seen.TUN {
		t.Error("the TUN setting did not reach the registration")
	}
	if seen.CapabilityHint == "" {
		t.Error("a TUN registration without the capability got no hint")
	}
	if !strings.Contains(out, "systemd-system") {
		t.Errorf("the output does not report the system unit: %q", out)
	}
}
