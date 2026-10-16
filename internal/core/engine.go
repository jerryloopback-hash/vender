// Package core owns the proxy engine: it renders vender's own configuration
// into sing-box's JSON schema and supervises the sing-box child process.
//
// The package split is deliberate. render.go is pure and deterministic (which
// is what makes it golden-testable), binary.go knows only where an engine
// binary may live, and singbox.go owns the process lifecycle. The only
// OS-specific code is the lock and the child process group, each behind a build
// tag in this package, so everything above core stays platform-free.
//
// M1 implemented the contract M0 froze: WriteConfig writes
// <RunDir>/singbox.json atomically, Start spawns `sing-box run -c <path>` and
// returns once the mixed inbound accepts TCP connections, and Logs streams the
// child's output until it exits (closing the channel then, so consumers do not
// leak).
package core

import (
	"errors"
	"fmt"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/rules"
)

// ConfigFileName is the generated sing-box configuration inside the run dir.
const ConfigFileName = "singbox.json"

// RuntimeConfig is the fully resolved input for one engine run: vender's
// global settings plus the server selected in config.yaml. It is passed to
// Engine.WriteConfig, which renders it into sing-box's own JSON schema.
type RuntimeConfig struct {
	// Config is the global vender configuration (listen address, mode, log level).
	Config config.Config `json:"config"`
	// Server is the selected outbound. It is required for mode rule/global and
	// may be nil for mode direct or when selected_server is "auto".
	Server *config.Server `json:"server,omitempty"`
	// Servers is every configured server, in servers.yaml order. It is only
	// consulted when selected_server is the "auto" sentinel, where it becomes
	// the member list of the URLTest failover group.
	Servers []config.Server `json:"servers,omitempty"`
	// UserRules is the parsed rules.yaml, or nil when the file is absent. Its
	// lists are merged ahead of the built-in policy.
	UserRules *rules.UserRules `json:"user_rules,omitempty"`
	// BinaryPath is the resolved sing-box executable (config.Engine.Binary,
	// else bundled next to the vender executable, else PATH).
	BinaryPath string `json:"binary_path"`
	// RunDir is where the generated config, lock file and logs live. It belongs
	// to the runtime (cache) directory, not the config directory.
	RunDir string `json:"run_dir"`

	// ClashListen enables sing-box's experimental Clash API when non-empty: a
	// "host:port" address the API listens on. The daemon uses it to read live
	// traffic counters and to measure proxy latency. Empty means "do not emit
	// the experimental section at all", which is what the M1/M2 golden files
	// and any hand-written config expect.
	ClashListen string `json:"clash_listen,omitempty"`
	// ClashSecret is the bearer token the Clash API requires. It is written
	// into the generated config (which is 0600 for exactly this reason) and is
	// never logged.
	ClashSecret string `json:"clash_secret,omitempty"`
}

// Validate rejects a runtime configuration that cannot produce a working
// engine: no server in a proxying mode, a bad listen address, or no run dir.
func (rc RuntimeConfig) Validate() error {
	if rc.RunDir == "" {
		return errors.New("core: RuntimeConfig.RunDir must not be empty")
	}
	if err := rc.Config.Validate(); err != nil {
		return err
	}
	if rc.UserRules != nil {
		if err := rc.UserRules.Validate(); err != nil {
			return fmt.Errorf("core: %w", err)
		}
	}
	if rc.Config.Proxy.Mode != config.ModeDirect {
		switch {
		case rc.Config.IsAutoServer():
			if len(rc.Servers) == 0 {
				return fmt.Errorf("core: selected_server %q needs at least one server in servers.yaml", config.SelectedServerAuto)
			}
		case rc.Server == nil:
			return fmt.Errorf("core: no server selected (required for proxy mode %q)", rc.Config.Proxy.Mode)
		}
	}
	if rc.Server != nil {
		if err := rc.Server.Validate(); err != nil {
			return err
		}
	}
	for i := range rc.Servers {
		if err := rc.Servers[i].Validate(); err != nil {
			return err
		}
	}
	if rc.ClashListen != "" {
		if err := validateClashListen(rc.ClashListen); err != nil {
			return err
		}
		if rc.ClashSecret == "" {
			return errors.New("core: RuntimeConfig.ClashListen requires a ClashSecret")
		}
	}
	// The TUN block is validated by config.Config.Validate; what only core can
	// check is that its rule-set references point at sets vender embeds.
	if err := validateTunRuleSets(rc.Config); err != nil {
		return err
	}
	return nil
}

// Engine is the single seam between vender and its proxy engine. M1 implements
// it over a managed sing-box subprocess.
type Engine interface {
	// WriteConfig renders rc into the engine's own configuration file
	// (<RunDir>/singbox.json) and writes it atomically.
	WriteConfig(rc RuntimeConfig) error
	// Start launches the engine and returns once the listener is usable.
	Start() error
	// Stop terminates the engine gracefully, escalating to a kill after a
	// short grace period.
	Stop() error
	// Logs streams engine output lines until the process exits.
	Logs() <-chan string
	// Healthy reports whether the engine is running and its inbound listener
	// accepts connections.
	Healthy() bool
}

// New returns the M1 engine: an implementation that renders sing-box JSON and
// supervises a sing-box child process. The returned engine is stopped; call
// WriteConfig, then Start.
func New(rc RuntimeConfig) (Engine, error) {
	if err := rc.Validate(); err != nil {
		return nil, err
	}
	if rc.BinaryPath == "" {
		return nil, errors.New("core: RuntimeConfig.BinaryPath is empty (resolve it with core.ResolveBinary)")
	}
	return newEngine(rc), nil
}
