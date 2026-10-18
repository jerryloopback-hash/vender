// Package control implements vender's daemon control channel: the local API
// the TUI (and, later, any other client) uses to talk to a running daemon.
//
// # Why a channel at all
//
// The daemon is authoritative (see docs/ARCHITECTURE.md §2): it owns the
// sing-box process and the rendered engine configuration. Some operations
// cannot be expressed as files in the run directory — switching the active
// server while the engine runs, tailing engine output as it happens, streaming
// live traffic counters — so M3 adds a real IPC channel. From M3 on, clients
// stop reading the generated config directly and ask the daemon instead.
//
// # Transport
//
// TCP on 127.0.0.1, with an OS-assigned port and a random 128-bit token. The
// daemon writes {pid, addr, token} to <run_dir>/control.json (mode 0600) and
// removes it on exit.
//
// A Unix domain socket (Linux) / named pipe (Windows) would be the more
// idiomatic choice, but it would mean two implementations behind build tags
// whose security properties differ (a unix socket is protected by the file
// mode; a Windows named pipe by its DACL and by the client's ability to open
// it). Loopback TCP needs neither, behaves identically on both platforms, and
// can be tested the same way on both. The token plus loopback binding is what
// keeps it private: a remote host cannot reach 127.0.0.1, and every request
// must present the token, which lives in a 0600 file inside a 0700 directory.
//
// # Wire format
//
// Newline-delimited JSON, one request per line, exactly one response per
// request. Every message is versioned with {"v":1,...}; a client that speaks a
// different version is rejected with a plain error instead of being
// misparsed.
//
//	-> {"v":1,"auth":"<token>","id":7,"cmd":"select","args":{"name":"LA"}}
//	<- {"v":1,"id":7,"ok":true,"data":{...}}
//	<- {"v":1,"id":7,"ok":false,"kind":"not_found","error":"server not found: ..."}
//
// A subscription is a request whose response acknowledges the stream and whose
// connection is then dedicated to it: the server emits one event frame per
// item and never expects another request on that connection.
//
//	-> {"v":1,"auth":"<token>","cmd":"traffic"}
//	<- {"v":1,"ok":true,"stream":"traffic"}
//	<- {"v":1,"stream":"traffic","data":{"up":1024,"down":65536}}
//	<- {"v":1,"stream":"traffic","data":{"up":2048,"down":131072}}
//
// Callers should use Client (and Controller on the server side) rather than
// encoding the frames by hand; the types below are exported so tests and
// alternate clients (a future `vender status --json`, an editor plugin) can.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the protocol version. It is carried on every message so a
// mismatched client fails loudly instead of being misinterpreted.
const Version = 1

// Commands accepted as a plain request/response call.
const (
	CmdPing    = "ping"
	CmdStatus  = "status"
	CmdServers = "servers"
	CmdSelect  = "select"
	CmdMode    = "mode"
	CmdTun     = "tun"
	CmdLatency = "latency"
	CmdStart   = "start"
	CmdStop    = "stop"
	CmdRestart = "restart"
	CmdRules   = "rules"
	// CmdProxies reports the engine's outbound table: which failover member is
	// active and how each one is measuring.
	CmdProxies = "proxies"
	// CmdProfile switches the active profile: with selected_server "auto", the
	// failover pool is the active profile's servers, so switching profiles
	// changes which nodes the engine may use.
	CmdProfile = "profile"
)

// Streams. A subscription request takes over its connection.
const (
	StreamLogs    = "logs"
	StreamTraffic = "traffic"
)

// Error kinds carried in a failed response, so a client can react without
// matching on message text.
const (
	KindAuth       = "auth"        // missing/wrong token, or a version mismatch
	KindBadRequest = "bad_request" // unknown command, malformed args
	KindNotFound   = "not_found"   // no such server, no such stream
	KindBusy       = "busy"        // an operation is already in flight
	KindInternal   = "internal"    // the daemon failed; Error says why
)

// Request is one client request.
type Request struct {
	V    int             `json:"v"`
	Auth string          `json:"auth,omitempty"`
	ID   int             `json:"id,omitempty"`
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the reply to a Request. A response that acknowledges a
// subscription carries Stream and no Data.
type Response struct {
	V      int    `json:"v"`
	ID     int    `json:"id,omitempty"`
	OK     bool   `json:"ok"`
	Kind   string `json:"kind,omitempty"`
	Error  string `json:"error,omitempty"`
	Stream string `json:"stream,omitempty"`
	Data   any    `json:"data,omitempty"`
}

// Event is one frame of a subscription. KeepAlive frames are heartbeats with
// no payload (they exist so an idle stream is distinguishable from a hung
// connection) and must be skipped by consumers.
type Event struct {
	V         int    `json:"v"`
	Stream    string `json:"stream"`
	KeepAlive bool   `json:"ka,omitempty"`
	Data      any    `json:"data,omitempty"`
}

// --- payloads ---------------------------------------------------------------

// PingData is the cheapest possible "are you there, and which version" probe.
type PingData struct {
	Vender  string `json:"vender"`
	Proto   int    `json:"proto"`
	PID     int    `json:"pid"`
	RunDir  string `json:"runDir"`
	Started string `json:"started,omitempty"`
}

// StatusData describes the daemon and the engine it supervises. It is a
// snapshot: the values come from the daemon's own state, so it answers even
// while the engine is restarting.
type StatusData struct {
	Vender      string `json:"vender"`
	PID         int    `json:"pid"`
	RunDir      string `json:"runDir"`
	StartedAt   string `json:"startedAt,omitempty"`
	UptimeSecs  int    `json:"uptimeSecs"`
	Listen      string `json:"listen"`
	Mode        string `json:"mode"`
	Selected    string `json:"selected,omitempty"`
	SrvType     string `json:"srvType,omitempty"`
	SrvEndpoint string `json:"srvEndpoint,omitempty"`
	SrvDetail   string `json:"srvDetail,omitempty"`

	EngineRunning bool   `json:"engineRunning"`
	EnginePid     int    `json:"enginePid,omitempty"`
	EngineHealthy bool   `json:"engineHealthy"`
	EngineVersion string `json:"engineVersion,omitempty"`
	Restarting    bool   `json:"restarting"`
	LastError     string `json:"lastError,omitempty"`

	ClashListen string `json:"clashListen,omitempty"`
	ServerCount int    `json:"serverCount"`

	// Profile is the active profile, and ProfileServers is how many servers
	// `selected_server: auto` scopes to. Profiles lists every known profile
	// name, so a client can cycle them without reading profiles.yaml itself.
	Profile        string   `json:"profile,omitempty"`
	ProfileServers int      `json:"profileServers"`
	Profiles       []string `json:"profiles,omitempty"`

	// Tun is the TUN inbound's state (M6). It travels in every status reply so
	// a client never has to guess whether system-wide capture is on, and so
	// the TUI can show the *daemon's* privilege verdict rather than its own.
	Tun TunStatus `json:"tun"`
}

// TunStatus describes the optional TUN inbound.
type TunStatus struct {
	// Enabled is the configuration setting (config.yaml engine.tun.enabled).
	Enabled bool `json:"enabled"`
	// Active reports whether the engine that is running now was rendered with a
	// TUN inbound. It differs from Enabled while the engine is stopped or after
	// an enable that could not be applied.
	Active bool `json:"active"`
	// Stack, Interface, MTU, AutoRoute and StrictRoute are the settings in
	// effect, for display.
	Stack       string `json:"stack,omitempty"`
	Interface   string `json:"interface,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
	AutoRoute   bool   `json:"autoRoute"`
	StrictRoute bool   `json:"strictRoute"`
	// PrivilegesOK reports whether this daemon can create a TUN device, and
	// PrivilegeDetail explains the verdict. They are answered in-process, which
	// is what makes them trustworthy: a client's own privileges may differ.
	PrivilegesOK    bool   `json:"privilegesOk"`
	PrivilegeDetail string `json:"privilegeDetail,omitempty"`
}

// NodeHealth is one failover member's state, as the engine reports it.
type NodeHealth struct {
	// Name is the outbound tag, which for a server is the server's own name.
	Name string `json:"name"`
	// Active reports whether this member is the one currently carrying traffic.
	Active bool `json:"active"`
	// DelayMs is the engine's last measured round trip through this member, or
	// 0 when it has not been measured (or the measurement failed).
	DelayMs int `json:"delayMs,omitempty"`
	// Alive reports whether the last measurement succeeded.
	Alive bool `json:"alive"`
}

// ProxiesData is the reply to CmdProxies: the failover group's state, plus one
// row per configured server. Nodes is empty (never nil, for JSON friendliness)
// when the daemon is not running or the engine has no Clash API.
type ProxiesData struct {
	// Auto reports whether selected_server is "auto", i.e. whether a failover
	// group is in play at all.
	Auto bool `json:"auto"`
	// Group is the proxy tag the policy uses, when there is a group.
	Group string `json:"group,omitempty"`
	// Current is the active member's tag (auto mode only).
	Current string `json:"current,omitempty"`
	// Nodes lists every configured server with its measured health.
	Nodes []NodeHealth `json:"nodes"`
	// Err is a non-fatal explanation (typically "the engine is not running").
	Err string `json:"err,omitempty"`
}

// ServerInfo is one row of the server list, as the daemon sees it.
type ServerInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Detail   string `json:"detail,omitempty"`
	Active   bool   `json:"active"`
	// Profile is the server's profile (config.ProfileManual when implicit).
	Profile string `json:"profile,omitempty"`
}

// ServersData is the reply to CmdServers.
type ServersData struct {
	Servers  []ServerInfo `json:"servers"`
	Selected string       `json:"selected,omitempty"`
}

// SelectArgs is the argument of CmdSelect.
type SelectArgs struct {
	Name string `json:"name"`
}

// ModeArgs is the argument of CmdMode.
type ModeArgs struct {
	Mode string `json:"mode"`
}

// TunArgs is the argument of CmdTun.
type TunArgs struct {
	Enabled bool `json:"enabled"`
}

// ProfileArgs is the argument of CmdProfile. Name is a profile name, or
// config.ProfileAll ("all") to scope auto failover to every server.
type ProfileArgs struct {
	Name string `json:"name"`
}

// LatencyArgs is the argument of CmdLatency. An empty Name means the active
// server.
type LatencyArgs struct {
	Name string `json:"name,omitempty"`
}

// LatencyData retains the v1 name/ms/err fields and adds direct ICMP metadata.
// ICMP silence does not establish proxy protocol availability.
type LatencyData struct {
	Name       string `json:"name"`
	Ms         int    `json:"ms,omitempty"`
	Err        string `json:"err,omitempty"`
	Method     string `json:"method,omitempty"`
	Sent       int    `json:"sent,omitempty"`
	Received   int    `json:"received,omitempty"`
	PacketLoss int    `json:"packetLoss,omitempty"`
	Address    string `json:"address,omitempty"`
	Interface  string `json:"interface,omitempty"`
}

// ApplyData is the reply to the mutating commands (select/mode/start/stop/
// restart): the resulting status, so a client does not need a second call.
type ApplyData struct {
	Action string     `json:"action"`
	Status StatusData `json:"status"`
}

// RuleInfo is one entry of the active routing policy.
type RuleInfo struct {
	Name     string `json:"name"`
	Matches  string `json:"matches"`
	Outbound string `json:"outbound"`
}

// DNSInfo is the active DNS policy summary.
type DNSInfo struct {
	Servers []string `json:"servers"`
	Rules   []string `json:"rules"`
	Final   string   `json:"final,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// RulesData is the reply to CmdRules: the policy the engine is running with.
type RulesData struct {
	Actions  []string   `json:"actions"`
	Rules    []RuleInfo `json:"rules"`
	Final    string     `json:"final"`
	DNS      DNSInfo    `json:"dns"`
	RuleSets []string   `json:"ruleSets"`
	Source   string     `json:"source"`
	Active   bool       `json:"active"` // false in global/direct mode, where no policy is applied
}

// LogLine is one engine output line. Source distinguishes the engine's own
// output from lifecycle notes the daemon adds ("engine stopped"), so a client
// can label or colour them differently.
type LogLine struct {
	Line   string `json:"line"`
	Time   string `json:"time,omitempty"`
	Source string `json:"src,omitempty"` // "engine" | "daemon"
}

// TrafficSample is one second of engine traffic. Up and Down are bytes per
// second; TotalUp/TotalDown are the cumulative counters since the engine
// started (0 when the engine does not report them yet).
type TrafficSample struct {
	Up        float64 `json:"up"`
	Down      float64 `json:"down"`
	TotalUp   int64   `json:"totalUp,omitempty"`
	TotalDown int64   `json:"totalDown,omitempty"`
}

// --- errors -----------------------------------------------------------------

// Protocol payload sources, used by LogLine.Source.
const (
	SourceEngine = "engine"
	SourceDaemon = "daemon"
)

// Error is a protocol error with a machine-readable kind.
type Error struct {
	Kind string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Errorf builds a protocol Error.
func Errorf(kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// KindOf reports the protocol kind of err, defaulting to KindInternal.
func KindOf(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return KindInternal
}
