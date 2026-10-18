package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/control"
)

func TestPingBatchBoundedSchedulingAndReentry(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenNodes)
	m.snap.Servers = nil
	for i := 0; i < 11; i++ {
		m.snap.Servers = append(m.snap.Servers, config.Server{Name: fmt.Sprintf("node-%d", i), Address: "203.0.113.1", Profile: fmt.Sprintf("profile-%d", i%3), Type: config.TypeHysteria2})
	}
	m.rebuildRows()
	beforeSelected, beforeMode, beforeTun := m.selectedName(), m.status.Mode, m.status.Tun
	next, cmd := m.handleNodesKey(keyMsg("T"))
	m = next.(Model)
	defer func() {
		if m.pingCancel != nil {
			m.pingCancel()
		}
	}()
	if cmd == nil || m.pingTotal != 11 || len(m.pingRunning) != 4 || len(m.pingPending) != 7 || m.busy != "" {
		t.Fatalf("bad start: total=%d running=%d queued=%d busy=%s", m.pingTotal, len(m.pingRunning), len(m.pingPending), m.busy)
	}
	id := m.pingID
	next, cmd = m.handleNodesKey(keyMsg("T"))
	m = next.(Model)
	if cmd != nil || m.pingID != id || !strings.Contains(m.toast, "already running") {
		t.Fatal("batch reentered")
	}
	for done := 0; done < 11; done++ {
		name := ""
		for n := range m.pingRunning {
			name = n
			break
		}
		if name == "" {
			t.Fatal("scheduler stalled")
		}
		next, _ = m.Update(latencyMsg{batch: id, name: name, data: control.LatencyData{Name: name, Ms: 12, Method: "icmp-direct", Sent: 3, Received: 3}})
		m = next.(Model)
		if len(m.pingRunning) > 4 || m.pingDone != done+1 {
			t.Fatal("concurrency/progress violated")
		}
	}
	if m.pingCancel != nil || m.pingDone != 11 || m.selectedName() != beforeSelected || m.status.Mode != beforeMode || m.status.Tun != beforeTun {
		t.Fatal("batch changed selection/mode/TUN or failed to finish")
	}
}
func TestPingCancelRejectsOldResultsAndKeepsNavigation(t *testing.T) {
	m := runningModel(t)
	m = m.switchScreen(screenNodes)
	next, _ := m.startPings([]string{"Interserver-LA", "Interserver-LA-Hy2"})
	m = next.(Model)
	id := m.pingID
	if m.servers[0].Latency != "pinging…" {
		t.Fatalf("missing progress row: %+v", m.servers[0])
	}
	next, _ = m.handleKey(keyMsg("tab"))
	m = next.(Model)
	if m.screen == screenNodes || m.pingCancel == nil {
		t.Fatal("navigation blocked or cancelled Ping")
	}
	next, _ = m.handleKey(keyMsg("esc"))
	m = next.(Model)
	if m.pingCancel != nil {
		t.Fatal("Esc did not cancel")
	}
	next, _ = m.startPings([]string{"Interserver-LA"})
	m = next.(Model)
	defer m.pingCancel()
	newID := m.pingID
	next, _ = m.Update(latencyMsg{batch: id, name: "Interserver-LA", data: control.LatencyData{Name: "Interserver-LA", Ms: 999}})
	m = next.(Model)
	next, _ = m.Update(pingDeadlineMsg{batch: id})
	m = next.(Model)
	if m.pingID != newID || m.pingDone != 0 || m.pingCancel == nil || m.latency["Interserver-LA"].Ms == 999 {
		t.Fatal("cancelled batch polluted new one")
	}
	next, _ = m.Update(pingDeadlineMsg{batch: newID})
	m = next.(Model)
	if m.pingCancel != nil {
		t.Fatal("batch deadline did not cancel")
	}
}
func TestAutoHealthNeverOverridesManualPing(t *testing.T) {
	m := runningModel(t)
	m.proxies = control.ProxiesData{Auto: true, Current: "Interserver-LA", Nodes: []control.NodeHealth{{Name: "Interserver-LA", Alive: true, Active: true, DelayMs: 42}, {Name: "Interserver-LA-Hy2", Alive: false}}}
	m.latency["Interserver-LA"] = control.LatencyData{Name: "Interserver-LA", Ms: 123, Method: "icmp-direct", Sent: 3, Received: 2, PacketLoss: 33}
	m.rebuildRows()
	if m.servers[0].Latency != "123 ms/33%" || m.servers[1].Latency != "" || !m.servers[0].Active {
		t.Fatalf("health overwritten Ping: %+v", m.servers)
	}
	if badge := m.latencyBadge(); strings.Contains(badge, "42") || !strings.Contains(badge, "123") {
		t.Fatal(badge)
	}
	if view := m.nodesView(); !strings.Contains(view, "automatic failover health only") {
		t.Fatal("URLTest health not labelled")
	}
}
