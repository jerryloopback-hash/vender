// Package config implements vender's on-disk configuration:
//
//   - config.yaml   global settings (proxy listener, engine path, log level)
//   - servers.yaml  the outbound server list (contains secrets, mode 0600)
//   - share-link parsing (vless:// and hysteria2://) into a Server
//
// The package is deliberately free of OS-specific code: locating the config
// directory is delegated to os.UserConfigDir, which resolves to
// ~/.config/vender on Linux and %AppData%\vender on Windows.
package config

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Log levels accepted in config.yaml.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// DefaultListen is the local mixed SOCKS5+HTTP inbound address.
//
// 7890 is deliberately avoided: it is the conventional port of a system-wide
// mihomo/clash instance, and colliding with one would make vender fail to bind
// on a host that already runs a proxy. 7897 is the clash "mixed" companion
// port, which such a host leaves free.
const DefaultListen = "127.0.0.1:7897"

// ProxyMode selects how traffic is routed by the engine.
type ProxyMode string

const (
	// ModeRule routes by rules (M2 supplies the built-in rule set).
	ModeRule ProxyMode = "rule"
	// ModeGlobal sends everything through the selected server.
	ModeGlobal ProxyMode = "global"
	// ModeDirect sends everything out without a proxy.
	ModeDirect ProxyMode = "direct"
)

// ProxyModes lists every accepted mode, in documentation order.
func ProxyModes() []ProxyMode { return []ProxyMode{ModeRule, ModeGlobal, ModeDirect} }

// SelectedServerAuto is the sentinel value of selected_server that asks vender
// to choose a server automatically: it renders a URLTest outbound group over
// every server in servers.yaml, so a degraded node is bypassed without a human
// switching anything. It is accepted wherever a server name is.
const SelectedServerAuto = "auto"

// IsAutoServer reports whether selected_server asks for automatic failover.
func (c *Config) IsAutoServer() bool {
	return strings.EqualFold(strings.TrimSpace(c.SelectedServer), SelectedServerAuto)
}

// Failover (URLTest) knob defaults and bounds, used when selected_server is
// "auto". They live here — next to the sentinel they belong to — rather than in
// core, so the documented config.yaml and the renderer cannot drift apart.
const (
	// DefaultFailoverInterval is how often each member is probed when
	// engine.failover_interval is unset. 30s is short enough that a node going
	// bad stops carrying traffic in well under the ~1m a "1m" interval could
	// take, without probing so often that the probes themselves load a fragile
	// link (each probe is sent *through* its member).
	DefaultFailoverInterval = "30s"
	// MinFailoverInterval and MaxFailoverInterval bound engine.failover_interval.
	MinFailoverInterval = 5 * time.Second
	MaxFailoverInterval = 10 * time.Minute
	// DefaultFailoverToleranceMS is the URLTest tolerance when
	// engine.failover_tolerance_ms is zero. It is above sing-box's own 50 ms
	// default so two nodes of similar latency do not flap.
	DefaultFailoverToleranceMS = 100
	// MaxFailoverToleranceMS bounds engine.failover_tolerance_ms.
	MaxFailoverToleranceMS = 5000
)

// Valid reports whether m is a known proxy mode.
func (m ProxyMode) Valid() bool {
	switch m {
	case ModeRule, ModeGlobal, ModeDirect:
		return true
	default:
		return false
	}
}

// ProxyConfig configures the local inbound.
type ProxyConfig struct {
	// Listen is a "host:port" address for the SOCKS5+HTTP inbound listener.
	Listen string `yaml:"listen"`
	// Mode is one of rule, global, direct.
	Mode ProxyMode `yaml:"mode"`
}

// EngineConfig configures the proxy engine (sing-box in M1).
type EngineConfig struct {
	// Binary is the path to the sing-box executable. Empty means "use the
	// bundled binary next to the vender executable, else look up PATH".
	Binary string `yaml:"binary"`
	// BindInterface pins outgoing connections of the proxy outbound to a
	// network interface by name (e.g. "eth0"). It is empty by default, which
	// lets the kernel choose. It exists for multi-homed hosts: a TUN/vpn
	// interface with a higher route metric would otherwise swallow the
	// connection to the server. Rendered as sing-box's outbound
	// "bind_interface".
	BindInterface string `yaml:"bind_interface"`
	// FailoverInterval is how often the URLTest failover group
	// (selected_server: auto) re-probes each member, as a Go duration string
	// ("30s", "1m30s"). It is only consulted in auto mode. Empty means the
	// default, DefaultFailoverInterval.
	//
	// Each probe is a real HTTP request sent *through* the member (the group's
	// "url"), so the interval is a trade-off: shorter reacts faster to a node
	// going bad, but adds probe traffic to every member and, on a fragile link,
	// can make the link busier than the traffic it is protecting.
	FailoverInterval string `yaml:"failover_interval"`
	// FailoverToleranceMS is the latency difference, in milliseconds, that
	// makes the URLTest group switch members. 0 means the default,
	// DefaultFailoverToleranceMS. A value above sing-box's own 50 ms default
	// stops two nodes of similar latency from flapping back and forth on every
	// jittery sample.
	FailoverToleranceMS int `yaml:"failover_tolerance_ms"`
	// Tun is the optional TUN inbound (§M6): transparent, system-wide traffic
	// capture. Off by default.
	Tun TunConfig `yaml:"tun"`
}

// FailoverIntervalDuration returns the configured probe interval. It falls back
// to DefaultFailoverInterval when the field is empty or unparsable, so a
// hand-built Config still renders a usable group; a config that went through
// Validate can never take the unparsable branch.
func (e EngineConfig) FailoverIntervalDuration() time.Duration {
	if v := strings.TrimSpace(e.FailoverInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	d, _ := time.ParseDuration(DefaultFailoverInterval)
	return d
}

// FailoverTolerance returns the configured URLTest tolerance, falling back to
// the engine default when the field is zero (its "use the default" value).
func (e EngineConfig) FailoverTolerance() int {
	if e.FailoverToleranceMS == 0 {
		return DefaultFailoverToleranceMS
	}
	return e.FailoverToleranceMS
}

// Validate checks the engine-level failover knobs. An empty failover_interval
// means "use the default" and is accepted; a zero failover_tolerance_ms is the
// documented "engine default" and is likewise accepted. A disabled engine has
// no such thing as off here — the knobs are inert unless selected_server is
// "auto", but they are cheap to check and a typo should be reported whenever
// the file is read.
func (e EngineConfig) Validate() []string {
	var problems []string

	if v := strings.TrimSpace(e.FailoverInterval); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf(
				"engine.failover_interval %q is not a duration (use forms like %q or \"1m30s\")", v, DefaultFailoverInterval))
		case d < MinFailoverInterval || d > MaxFailoverInterval:
			problems = append(problems, fmt.Sprintf(
				"engine.failover_interval %s is out of range: use %s to %s (default %s)",
				d, MinFailoverInterval, MaxFailoverInterval, DefaultFailoverInterval))
		}
	}

	if e.FailoverToleranceMS < 0 || e.FailoverToleranceMS > MaxFailoverToleranceMS {
		problems = append(problems, fmt.Sprintf(
			"engine.failover_tolerance_ms %d is out of range: use 0 to %d (0 means the engine default, %d)",
			e.FailoverToleranceMS, MaxFailoverToleranceMS, DefaultFailoverToleranceMS))
	}

	return problems
}

// Config is the global vender configuration (config.yaml).
type Config struct {
	LogLevel string       `yaml:"log_level"`
	Proxy    ProxyConfig  `yaml:"proxy"`
	Engine   EngineConfig `yaml:"engine"`
	// SelectedServer names the active entry in servers.yaml. Empty means none.
	SelectedServer string `yaml:"selected_server"`
}

// Default returns the built-in defaults. Save writes this verbatim, which is
// what the documented config.yaml sample shows.
func Default() *Config {
	return &Config{
		LogLevel: LogLevelInfo,
		Proxy: ProxyConfig{
			Listen: DefaultListen,
			Mode:   ModeRule,
		},
		Engine: EngineConfig{
			Binary:              "",
			FailoverInterval:    DefaultFailoverInterval,
			FailoverToleranceMS: DefaultFailoverToleranceMS,
			Tun:                 DefaultTun(),
		},
		SelectedServer: "",
	}
}

// Normalize fills zero values with defaults so a partial config.yaml (or a
// zero-value Config) behaves like a documented one.
func (c *Config) Normalize() {
	def := Default()
	if c.LogLevel == "" {
		c.LogLevel = def.LogLevel
	}
	if c.Proxy.Listen == "" {
		c.Proxy.Listen = def.Proxy.Listen
	}
	if c.Proxy.Mode == "" {
		c.Proxy.Mode = def.Proxy.Mode
	}
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	c.Proxy.Listen = strings.TrimSpace(c.Proxy.Listen)
	c.Proxy.Mode = ProxyMode(strings.ToLower(strings.TrimSpace(string(c.Proxy.Mode))))
	c.Engine.Binary = strings.TrimSpace(c.Engine.Binary)
	c.Engine.BindInterface = strings.TrimSpace(c.Engine.BindInterface)
	c.Engine.FailoverInterval = strings.TrimSpace(c.Engine.FailoverInterval)
	if c.Engine.FailoverInterval == "" {
		c.Engine.FailoverInterval = def.Engine.FailoverInterval
	}
	if c.Engine.FailoverToleranceMS == 0 {
		c.Engine.FailoverToleranceMS = def.Engine.FailoverToleranceMS
	}
	c.Engine.Tun.Normalize()
	c.SelectedServer = strings.TrimSpace(c.SelectedServer)
	if c.IsAutoServer() {
		c.SelectedServer = SelectedServerAuto
	}
}

// Validate checks the config in isolation, without consulting servers.yaml.
func (c *Config) Validate() error {
	var problems []string

	switch c.LogLevel {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
	default:
		problems = append(problems, fmt.Sprintf("log_level %q is not one of debug, info, warn, error", c.LogLevel))
	}

	if !c.Proxy.Mode.Valid() {
		problems = append(problems, fmt.Sprintf("proxy.mode %q is not one of rule, global, direct", c.Proxy.Mode))
	}

	if err := validateListen(c.Proxy.Listen); err != nil {
		problems = append(problems, "proxy.listen: "+err.Error())
	}

	if invalidInterfaceName(c.Engine.BindInterface, runtime.GOOS) {
		problems = append(problems, fmt.Sprintf("engine.bind_interface %q is not a valid interface name (spaces are supported only on Windows)", c.Engine.BindInterface))
	}

	problems = append(problems, c.Engine.Validate()...)

	problems = append(problems, c.Engine.Tun.Validate()...)

	if len(problems) > 0 {
		return &ValidationError{Subject: "config.yaml", Problems: problems}
	}
	return nil
}

// ValidateAgainst additionally checks that SelectedServer exists (or is the
// "auto" sentinel, which requires at least one server to choose from).
func (c *Config) ValidateAgainst(ss *ServerSet) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.IsAutoServer() {
		if c.Proxy.Mode != ModeDirect && (ss == nil || ss.Len() == 0) {
			return &ValidationError{
				Subject:  "config.yaml",
				Problems: []string{fmt.Sprintf("selected_server %q needs at least one server in servers.yaml", SelectedServerAuto)},
			}
		}
		return nil
	}
	if c.SelectedServer != "" {
		if _, ok := ss.Get(c.SelectedServer); !ok {
			return &ValidationError{
				Subject:  "config.yaml",
				Problems: []string{fmt.Sprintf("selected_server %q is not present in servers.yaml", c.SelectedServer)},
			}
		}
	}
	return nil
}

// Selection resolves selected_server against the server set:
//
//   - direct mode uses no server at all: (nil, nil, nil);
//   - the "auto" sentinel returns the whole list as failover candidates,
//     with a nil single server;
//   - otherwise the named server is returned with a nil list.
//
// It is the single place the daemon and the controller agree on what
// selected_server means, so `core` never has to know about the sentinel.
func (c *Config) Selection(ss *ServerSet) (*Server, []Server, error) {
	return c.SelectionInProfile(ss, "")
}

// SelectionInProfile is Selection with the failover pool scoped to one
// profile: with selected_server "auto", the URLTest group is built over the
// servers of activeProfile only (an empty activeProfile means "every server",
// which is the pre-M7 behaviour and what an absent profiles.yaml yields).
//
// A named server is resolved against the whole set regardless of profile: a
// user who names a server means that server.
func (c *Config) SelectionInProfile(ss *ServerSet, activeProfile string) (*Server, []Server, error) {
	if c.Proxy.Mode == ModeDirect {
		return nil, nil, nil
	}
	if c.IsAutoServer() {
		var pool []Server
		if ss != nil {
			pool = ss.InProfile(activeProfile)
		}
		if len(pool) == 0 {
			profile := strings.TrimSpace(activeProfile)
			if profile == "" {
				return nil, nil, fmt.Errorf("selected_server %q needs at least one server in servers.yaml", SelectedServerAuto)
			}
			return nil, nil, fmt.Errorf("selected_server %q needs at least one server in profile %q (switch profiles with `vender profiles use`, or add nodes)",
				SelectedServerAuto, profile)
		}
		return nil, pool, nil
	}
	if c.SelectedServer == "" {
		return nil, nil, fmt.Errorf("no server selected for proxy mode %q:\n"+
			"  import one with `vender import '<share-link>'`\n"+
			"  or pick one (or %q) in config.yaml", c.Proxy.Mode, SelectedServerAuto)
	}
	srv, ok := ss.Get(c.SelectedServer)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q (import it, or fix selected_server in config.yaml)", ErrNotFound, c.SelectedServer)
	}
	return srv, nil, nil
}

// Clone returns a deep copy (the TUN block owns a pointer and three slices, so
// a shallow copy would let two configs share them).
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Engine.Tun = c.Engine.Tun.Clone()
	return &cp
}

// MarshalYAML keeps the emitted key order stable and human friendly.
func (c *Config) MarshalYAML() (any, error) {
	type raw Config // avoid recursing into this method
	return raw(*c), nil
}

func validateListen(addr string) error {
	if addr == "" {
		return errors.New("must not be empty")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be a host:port address (got %q): %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("host must not be empty in %q (a wildcard listener would expose the proxy)", addr)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return fmt.Errorf("wildcard host in %q would expose the unauthenticated proxy; use a loopback address", addr)
	}
	if err := validatePortString(port); err != nil {
		return err
	}
	return nil
}

func validatePortString(s string) error {
	p, err := parsePort(s)
	if err != nil {
		return fmt.Errorf("invalid port %q", s)
	}
	if p < 1 || p > 65535 {
		return fmt.Errorf("port %d out of range 1-65535", p)
	}
	return nil
}

// loadYAML decodes a YAML file leniently (unknown keys are ignored so that a
// config written by a newer vender still loads in an older one).
func loadYAML(data []byte, out any) error {
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	return nil
}
