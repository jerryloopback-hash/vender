// Package core will own the proxy engine: generating a sing-box configuration
// from vender's own config and managing the sing-box child process.
//
// M0 ships only the contract below (the interface, the resolved runtime
// configuration, and an error-returning constructor), so that the CLI, the TUI
// (M3) and the rule engine (M2) can be written against a stable API. M1 fills
// in the implementation: write <rundir>/singbox.json, spawn `sing-box run -c
// <path>`, stream its stderr into Logs, and treat a healthy process whose
// first log lines report listeners bound as Healthy.
package core

import (
	"errors"
	"fmt"

	"github.com/ll-lite/vender/internal/config"
)

// ErrNotImplemented is returned by every entry point until M1 lands.
var ErrNotImplemented = errors.New("core: engine management is not implemented in this milestone (M1)")

// ConfigFileName is the generated sing-box configuration inside the run dir.
const ConfigFileName = "singbox.json"

// RuntimeConfig is the fully resolved input for one engine run: vender's
// global settings plus the server selected in config.yaml. It is passed to
// Engine.WriteConfig, which renders it into sing-box's own JSON schema.
type RuntimeConfig struct {
	// Config is the global vender configuration (listen address, mode, log level).
	Config config.Config `json:"config"`
	// Server is the selected outbound. It is required for mode rule/global and
	// may be nil for mode direct.
	Server *config.Server `json:"server,omitempty"`
	// BinaryPath is the resolved sing-box executable (config.Engine.Binary,
	// else bundled next to the vender executable, else PATH).
	BinaryPath string `json:"binary_path"`
	// RunDir is where the generated config, lock file and logs live. It belongs
	// to the runtime (cache) directory, not the config directory.
	RunDir string `json:"run_dir"`
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
	if rc.Config.Proxy.Mode != config.ModeDirect && rc.Server == nil {
		return fmt.Errorf("core: no server selected (required for proxy mode %q)", rc.Config.Proxy.Mode)
	}
	if rc.Server != nil {
		if err := rc.Server.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Engine is the single seam between vender and its proxy engine. M1 implements
// it over a managed sing-box subprocess; tests in M1 will use a fake.
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

// New returns the M1 engine implementation. Until then it fails loudly rather
// than returning a half-working engine.
func New(rc RuntimeConfig) (Engine, error) {
	_ = rc
	return nil, ErrNotImplemented
}
