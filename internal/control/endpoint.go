package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ll-lite/vender/internal/privatefs"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// EndpointFileName is the daemon's published control endpoint inside the run
// directory.
const EndpointFileName = "control.json"

// endpointFileMode: the file carries the control token, so it is as sensitive
// as servers.yaml.
const endpointFileMode fs.FileMode = 0o600

// Endpoint is what a daemon publishes so a client can find and authenticate to
// it. It lives in <run_dir>/control.json next to the lock file: the lock says
// "a daemon is up", the endpoint says "here is how to talk to it".
type Endpoint struct {
	V       int    `json:"v"`
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`  // host:port on loopback
	Token   string `json:"token"` // random, regenerated on every daemon start
	Started string `json:"started,omitempty"`
	RunDir  string `json:"runDir,omitempty"`
	Vender  string `json:"vender,omitempty"`
}

// Valid reports whether the endpoint is complete enough to dial.
func (e Endpoint) Valid() bool {
	if e.V != Version || e.Addr == "" || e.Token == "" {
		return false
	}
	host, port, err := net.SplitHostPort(e.Addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && p > 0 && p <= 65535
}

// EndpointPath returns the control endpoint file inside runDir.
func EndpointPath(runDir string) string {
	return filepath.Join(runDir, EndpointFileName)
}

// WriteEndpoint publishes ep atomically with mode 0600.
func WriteEndpoint(runDir string, ep Endpoint) error {
	if err := privatefs.EnsureDir(runDir); err != nil {
		return fmt.Errorf("control: create run dir %s: %w", runDir, err)
	}
	data, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return fmt.Errorf("control: encode endpoint: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(EndpointPath(runDir), data, endpointFileMode)
}

// ReadEndpoint loads the published endpoint from runDir.
//
// A missing file yields fs.ErrNotExist (wrapped), which is how every caller
// distinguishes "no daemon" from "a broken endpoint file" — the TUI needs to
// tell those apart to print either "start it with vender daemon" or the
// actual parse error.
func ReadEndpoint(runDir string) (Endpoint, error) {
	if err := privatefs.Protect(EndpointPath(runDir)); err != nil {
		return Endpoint{}, err
	}
	data, err := os.ReadFile(EndpointPath(runDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Endpoint{}, fmt.Errorf("control: no daemon is running in %s: %w", runDir, err)
		}
		return Endpoint{}, fmt.Errorf("control: read %s: %w", EndpointPath(runDir), err)
	}
	var ep Endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return Endpoint{}, fmt.Errorf("control: parse %s: %w", EndpointPath(runDir), err)
	}
	if !ep.Valid() {
		return Endpoint{}, fmt.Errorf("control: %s is not a usable endpoint (v=%d addr=%q)", EndpointPath(runDir), ep.V, ep.Addr)
	}
	return ep, nil
}

// RemoveEndpoint deletes the endpoint file, ignoring a missing file. The
// daemon calls it on exit so a stale token cannot outlive its daemon.
func RemoveEndpoint(runDir string) {
	_ = os.Remove(EndpointPath(runDir))
}
