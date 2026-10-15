package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
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

func TestCLIMilestoneStubs(t *testing.T) {
	tests := []struct {
		args      []string
		milestone string
	}{
		{args: []string{"daemon"}, milestone: "M1"},
		{args: []string{"status"}, milestone: "M1"},
		{args: []string{"proxy", "on"}, milestone: "M1"},
		{args: []string{"proxy", "off"}, milestone: "M1"},
		{args: []string{"tui"}, milestone: "M3"},
		{args: []string{"autostart", "on"}, milestone: "M4"},
		{args: []string{"autostart", "off"}, milestone: "M4"},
		{args: []string{"autostart", "status"}, milestone: "M4"},
	}

	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			out, err := runCLI(t, tt.args...)
			if err == nil {
				t.Fatalf("%v = nil error, want a not-implemented error (output: %q)", tt.args, out)
			}
			if !strings.Contains(err.Error(), "not implemented in this milestone") {
				t.Errorf("error = %q, want it to say \"not implemented in this milestone\"", err)
			}
			if !strings.Contains(err.Error(), "("+tt.milestone+")") {
				t.Errorf("error = %q, want it to name %s", err, tt.milestone)
			}
		})
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
		{name: "unsupported scheme", link: "vmess://" + testUUID + "@" + testHost + ":443#x", wantSub: "unsupported share link scheme"},
		{name: "garbage", link: "hello", wantSub: "missing scheme"},
		{name: "bad uuid", link: "vless://nope@" + testHost + ":443#x", wantSub: "not a UUID"},
		{name: "reality without public key", link: "vless://" + testUUID + "@" + testHost + ":443?security=reality&sni=www.example.com#x", wantSub: "public_key"},
		{name: "hy2 without password", link: "hysteria2://" + testHost + ":8443/#x", wantSub: "missing password"},
		{name: "ws transport is out of scope", link: "vless://" + testUUID + "@" + testHost + ":443?security=tls&sni=www.example.com&type=ws#x", wantSub: "tcp only"},
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
