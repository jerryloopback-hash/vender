package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// documentedServersYAML is the schema documented in docs/ARCHITECTURE.md and
// README.md, with dummy values. Loading it must work unchanged: it proves the
// YAML keys (public_key, short_id, obfs_password...) match the documentation.
const documentedServersYAML = `servers:
  - name: example-vless
    type: vless
    server: 203.0.113.10
    port: 443
    uuid: "11111111-1111-1111-1111-111111111111"
    flow: xtls-rprx-vision
    security: reality
    sni: www.example.com
    fingerprint: chrome
    public_key: DUMMY-FAKE-PUBLIC-KEY-FOR-TESTS-ONLY-0000000000
    short_id: 0123456789abcdef
    network: tcp
    header_type: none
    password: ""
    obfs: ""
    obfs_password: ""
    insecure: false
  - name: example-hy2
    type: hysteria2
    server: 198.51.100.7
    port: 8443
    password: "22222222-3333-4444-5555-666666666666"
    obfs: salamander
    obfs_password: cafebabe
    insecure: true
`

func TestLoadServersFromDocumentedSchema(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := os.WriteFile(store.ServersPath(), []byte(documentedServersYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ss, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if ss.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", ss.Len())
	}

	vless, ok := ss.Get("example-vless")
	if !ok {
		t.Fatal("example-vless not found")
	}
	wantVLESS := Server{
		Name: "example-vless", Type: TypeVLESS, Address: "203.0.113.10", Port: 443,
		UUID: testUUID, Flow: "xtls-rprx-vision", Security: SecurityReality,
		SNI: "www.example.com", Fingerprint: "chrome", PublicKey: testPBK,
		ShortID: testSID, Network: "tcp", HeaderType: "none",
	}
	if !reflect.DeepEqual(*vless, wantVLESS) {
		t.Errorf("vless entry mismatch\n got: %#v\nwant: %#v", *vless, wantVLESS)
	}

	hy2, ok := ss.Get("example-hy2")
	if !ok {
		t.Fatal("example-hy2 not found")
	}
	wantHy2 := Server{
		Name: "example-hy2", Type: TypeHysteria2, Address: "198.51.100.7", Port: 8443,
		Password: testPW, Obfs: "salamander", ObfsPassword: "cafebabe", Insecure: true,
	}
	if !reflect.DeepEqual(*hy2, wantHy2) {
		t.Errorf("hysteria2 entry mismatch\n got: %#v\nwant: %#v", *hy2, wantHy2)
	}
}

func TestLoadServersMissingFileIsEmpty(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	ss, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if ss.Len() != 0 {
		t.Errorf("Len() = %d, want 0", ss.Len())
	}
	if ss.Servers == nil {
		t.Error("Servers is nil, want an empty slice so YAML emits `servers: []`")
	}
}

func TestLoadServersReportsBrokenYAML(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := os.WriteFile(store.ServersPath(), []byte("servers: [{name: x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := store.LoadServers(); err == nil {
		t.Fatal("LoadServers() = nil, want a parse error")
	}
}

func TestLoadServersRejectsInvalidEntries(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	body := "servers:\n  - name: broken\n    type: hysteria2\n    server: 203.0.113.10\n    port: 8443\n"
	if err := os.WriteFile(store.ServersPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := store.LoadServers()
	if err == nil {
		t.Fatal("LoadServers() = nil, want a validation error for a missing password")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error = %q, want it to mention the missing password", err)
	}
}

func TestLoadServersToleratesUnknownKeys(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	body := "servers:\n  - name: n\n    type: hysteria2\n    server: 203.0.113.10\n    port: 8443\n" +
		"    password: " + testPW + "\n    future_field: 42\n"
	if err := os.WriteFile(store.ServersPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ss, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if ss.Len() != 1 {
		t.Errorf("Len() = %d, want 1", ss.Len())
	}
}

func TestServersRoundTripAndPermissions(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	ss := NewServerSet()

	vless := Server{
		Name: "vless-node", Type: TypeVLESS, Address: testHost, Port: 443,
		UUID: testUUID, Flow: "xtls-rprx-vision", Security: SecurityReality,
		SNI: "www.example.com", Fingerprint: "chrome", PublicKey: testPBK,
		ShortID: testSID, Network: "tcp", HeaderType: "none",
	}
	hy2 := Server{
		Name: "hy2-node", Type: TypeHysteria2, Address: "198.51.100.7", Port: 8443,
		Password: testPW, SNI: "www.example.com", Obfs: "salamander",
		ObfsPassword: "cafebabe", Insecure: true, ALPN: []string{"h3"},
	}
	for _, srv := range []Server{vless, hy2} {
		if err := ss.Add(srv, false); err != nil {
			t.Fatalf("Add(%s): %v", srv.Name, err)
		}
	}

	if err := store.SaveServers(ss); err != nil {
		t.Fatalf("SaveServers: %v", err)
	}

	// Secrets are protected.
	info, err := os.Stat(store.ServersPath())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != secretFileMode {
		t.Errorf("servers.yaml mode = %o, want %o", perm, secretFileMode)
	}

	first, err := os.ReadFile(store.ServersPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(first), serversHeader) {
		t.Errorf("servers.yaml does not start with the header:\n%s", first)
	}
	// A vless entry must not carry hysteria2 fields and vice versa.
	if strings.Contains(string(first), "obfs_password") && !strings.Contains(string(first), "hy2-node") {
		t.Error("unexpected obfs_password for a vless-only entry")
	}
	vlessSection := string(first[:strings.Index(string(first), "hy2-node")])
	for _, banned := range []string{"password:", "obfs:", "insecure:"} {
		if strings.Contains(vlessSection, banned) {
			t.Errorf("vless entry in servers.yaml contains %q:\n%s", banned, vlessSection)
		}
	}

	loaded, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if !reflect.DeepEqual(*loaded, *ss) {
		t.Errorf("round trip mismatch\n got: %#v\nwant: %#v", *loaded, *ss)
	}

	if err := store.SaveServers(loaded); err != nil {
		t.Fatalf("SaveServers (second): %v", err)
	}
	second, err := os.ReadFile(store.ServersPath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("servers.yaml is not byte stable\n first:\n%s\nsecond:\n%s", first, second)
	}
}

func TestSaveServersFixesLoosePermissions(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	ss := NewServerSet()
	if err := ss.Add(Server{Name: "n", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}, false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatalf("SaveServers: %v", err)
	}
	// Simulate a file that was chmod'ed to something world-readable.
	if err := os.Chmod(store.ServersPath(), 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatalf("SaveServers (second): %v", err)
	}
	info, err := os.Stat(store.ServersPath())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != secretFileMode {
		t.Errorf("mode = %o, want it tightened to %o", perm, secretFileMode)
	}
}

func TestSaveServersLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	ss := NewServerSet()
	if err := ss.Add(Server{Name: "n", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}, false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := store.SaveServers(ss); err != nil {
			t.Fatalf("SaveServers: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{ServersFileName}) {
		t.Errorf("config dir contains %v, want only %s", names, ServersFileName)
	}
}

func TestSaveServersRejectsInvalid(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	ss := &ServerSet{Servers: []Server{
		{Name: "bad", Type: TypeHysteria2, Address: testHost, Port: 8443},
	}}
	if err := store.SaveServers(ss); err == nil {
		t.Fatal("SaveServers() = nil, want a validation error")
	}
	if _, err := os.Stat(store.ServersPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an invalid server list was written: stat = %v", err)
	}
}

func TestStoreAddRemoveSelectServer(t *testing.T) {
	store := NewStoreAt(t.TempDir())

	added := Server{Name: "node-a", Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}
	ss, err := store.AddServer(added, false)
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if ss.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", ss.Len())
	}
	// The server must be on disk, not only in memory.
	onDisk, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if _, ok := onDisk.Get("node-a"); !ok {
		t.Fatal("node-a was not persisted")
	}

	if _, err := store.AddServer(Server{Name: "node-a", Type: TypeHysteria2, Address: testHost, Port: 8444, Password: testPW}, false); !errors.Is(err, ErrDuplicate) {
		t.Errorf("AddServer(duplicate) = %v, want ErrDuplicate", err)
	}
	if _, err := store.AddServer(Server{Name: "node-a", Type: TypeHysteria2, Address: testHost, Port: 8444, Password: testPW}, true); err != nil {
		t.Errorf("AddServer(overwrite) = %v, want nil", err)
	}
	onDisk, err = store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if srv, _ := onDisk.Get("node-a"); srv.Port != 8444 {
		t.Errorf("port = %d, want 8444 after an overwrite", srv.Port)
	}

	if err := store.SelectServer("node-a"); err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "node-a" {
		t.Errorf("SelectedServer = %q, want node-a", cfg.SelectedServer)
	}
	// Case-insensitive selection stores the canonical name.
	if err := store.SelectServer("NODE-A"); err != nil {
		t.Fatalf("SelectServer(case): %v", err)
	}
	if err := store.SelectServer("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SelectServer(ghost) = %v, want ErrNotFound", err)
	}

	removed, err := store.RemoveServer("NODE-A")
	if err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if removed.Name != "node-a" {
		t.Errorf("removed name = %q, want node-a", removed.Name)
	}
	if _, err := store.RemoveServer("node-a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveServer(again) = %v, want ErrNotFound", err)
	}
	// Removing the selected server clears the selection.
	cfg, err = store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "" {
		t.Errorf("SelectedServer = %q, want it cleared after removing the selected server", cfg.SelectedServer)
	}
	onDisk, err = store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if onDisk.Len() != 0 {
		t.Errorf("Len() = %d, want 0", onDisk.Len())
	}
}

func TestStoreSupersedingSelectionIsKept(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	for _, name := range []string{"a", "b"} {
		if _, err := store.AddServer(Server{Name: name, Type: TypeHysteria2, Address: testHost, Port: 8443, Password: testPW}, false); err != nil {
			t.Fatalf("AddServer(%s): %v", name, err)
		}
	}
	if err := store.SelectServer("b"); err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	if _, err := store.RemoveServer("a"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SelectedServer != "b" {
		t.Errorf("SelectedServer = %q, want b (removing another server must not clear it)", cfg.SelectedServer)
	}
}

func TestStoreImportFromShareLinkEndToEnd(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	link := fmt.Sprintf("vless://%s@%s:443?encryption=none&flow=xtls-rprx-vision&security=reality"+
		"&sni=www.example.com&fp=chrome&pbk=%s&sid=%s&type=tcp&headerType=none#Imported-Node",
		testUUID, testHost, testPBK, testSID)

	srv, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink: %v", err)
	}
	if _, err := store.AddServer(*srv, false); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	ss, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	got, ok := ss.Get("Imported-Node")
	if !ok {
		t.Fatal("Imported-Node not found")
	}
	// The stored server must render back to an equivalent link.
	roundTripped, err := ParseShareLink(got.ShareLink())
	if err != nil {
		t.Fatalf("ParseShareLink(ShareLink()): %v", err)
	}
	if !reflect.DeepEqual(*got, *roundTripped) {
		t.Errorf("link round trip through servers.yaml changed the server\n got: %#v\nwant: %#v", *roundTripped, *got)
	}
}

func TestEnsureDirCreatesParents(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "vender")
	store := NewStoreAt(dir)
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	store2 := NewStoreAt(dir)
	if err := store2.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir (second call): %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", dir)
	}
}
