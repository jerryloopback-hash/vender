package core

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
)

// Real-engine regression with local .invalid names only; no remote node or
// upstream DNS is used. Applications may retain the first run's DNS answers.
func TestFakeIPMappingSurvivesEngineRestart(t *testing.T) {
	binaryPath := findSingBox()
	if binaryPath == "" {
		t.Skip("no real sing-box available")
	}
	freeTCP := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		return addr
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dnsAddr := udp.LocalAddr().String()
	udp.Close()
	_, portText, _ := net.SplitHostPort(dnsAddr)
	port, _ := strconv.Atoi(portText)
	cfg := *config.Default()
	cfg.Proxy.Listen = freeTCP()
	rc := RuntimeConfig{Config: cfg, Server: checkTestServer(), BinaryPath: binaryPath, RunDir: t.TempDir(), ClashListen: freeTCP(), ClashSecret: "test-only-token"}
	e := newEngine(rc)
	t.Cleanup(func() { _ = e.Stop() })
	cachePath := filepath.Join(rc.RunDir, "fakeip.db")
	if err := os.WriteFile(cachePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.WriteConfig(rc); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(cachePath); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("FakeIP cache permissions = %o, want 600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(ConfigPath(rc.RunDir))
	var generated map[string]any
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	generated["inbounds"] = append(generated["inbounds"].([]any), map[string]any{
		"type": "direct", "tag": "test-dns", "listen": "127.0.0.1", "listen_port": port, "network": "udp",
	})
	data, _ = json.Marshal(generated)
	if err := os.WriteFile(ConfigPath(rc.RunDir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	query := func(name string) string {
		t.Helper()
		packet := make([]byte, 12)
		binary.BigEndian.PutUint16(packet[0:2], 123)
		binary.BigEndian.PutUint16(packet[2:4], 0x100)
		binary.BigEndian.PutUint16(packet[4:6], 1)
		for _, label := range strings.Split(name, ".") {
			packet = append(packet, byte(len(label)))
			packet = append(packet, label...)
		}
		packet = append(packet, 0, 0, 1, 0, 1)
		conn, err := net.Dial("udp", dnsAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(packet); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil || n < 16 || binary.BigEndian.Uint16(buf[6:8]) == 0 {
			t.Fatalf("no DNS answer: n=%d err=%v", n, err)
		}
		return net.IP(buf[n-4 : n]).String()
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	_ = query("alpha.debug.invalid")
	before := query("beta.debug.invalid")
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if after := query("beta.debug.invalid"); after != before {
		t.Fatalf("FakeIP reassigned after restart: before=%s after=%s", before, after)
	}
}
