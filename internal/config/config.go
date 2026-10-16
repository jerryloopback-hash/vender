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
	"strings"

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
	// Tun is the optional TUN inbound (§M6): transparent, system-wide traffic
	// capture. Off by default.
	Tun TunConfig `yaml:"tun"`
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
		Engine:         EngineConfig{Binary: "", Tun: DefaultTun()},
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

	if strings.ContainsAny(c.Engine.BindInterface, " \t/") {
		problems = append(problems, fmt.Sprintf("engine.bind_interface %q must be an interface name without spaces or slashes", c.Engine.BindInterface))
	}

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
	if c.Proxy.Mode == ModeDirect {
		return nil, nil, nil
	}
	if c.IsAutoServer() {
		if ss == nil || ss.Len() == 0 {
			return nil, nil, fmt.Errorf("selected_server %q needs at least one server in servers.yaml", SelectedServerAuto)
		}
		all := make([]Server, len(ss.Servers))
		copy(all, ss.Servers)
		return nil, all, nil
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
