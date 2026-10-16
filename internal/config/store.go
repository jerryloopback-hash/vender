package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// File and directory names inside the vender config directory.
const (
	// AppDirName is the directory created under os.UserConfigDir().
	AppDirName = "vender"
	// ConfigFileName holds global settings.
	ConfigFileName = "config.yaml"
	// ServersFileName holds the server list and its secrets.
	ServersFileName = "servers.yaml"
	// RulesFileName holds the optional per-user routing rules. Unlike the
	// other two it is not created at first run: an absent file means "the
	// built-in policy, unmodified".
	RulesFileName = "rules.yaml"

	// secretFileMode protects servers.yaml, which contains uuids, passwords
	// and REALITY public keys.
	secretFileMode fs.FileMode = 0o600
	// plainFileMode is used for config.yaml, which holds no secrets.
	plainFileMode fs.FileMode = 0o644
	// dirMode is used for the vender config directory.
	dirMode fs.FileMode = 0o700
)

// serversHeader is prepended to servers.yaml so the file explains its own
// permissions to anyone who stumbles on it.
const serversHeader = "# vender server list. Contains credentials: keep this file private (written with mode 0600).\n" +
	"# Managed by `vender import` / `vender servers rm`; hand-editing is supported.\n"

// DefaultDir returns the platform config directory for vender:
//
//	Linux   $XDG_CONFIG_HOME/vender  (default ~/.config/vender)
//	Windows %AppData%\vender
//
// It creates nothing; call Store.EnsureDir for that.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, AppDirName), nil
}

// Store reads and writes vender's configuration files in a directory. Tests
// construct one over a temp dir with NewStoreAt; the CLI uses NewStore.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at the platform config directory.
func NewStore() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// NewStoreAt returns a Store rooted at an explicit directory.
func NewStoreAt(dir string) *Store { return &Store{dir: dir} }

// Dir returns the configuration directory.
func (s *Store) Dir() string { return s.dir }

// ConfigPath returns the path of config.yaml.
func (s *Store) ConfigPath() string { return filepath.Join(s.dir, ConfigFileName) }

// ServersPath returns the path of servers.yaml.
func (s *Store) ServersPath() string { return filepath.Join(s.dir, ServersFileName) }

// RulesPath returns the path of the optional rules.yaml.
func (s *Store) RulesPath() string { return filepath.Join(s.dir, RulesFileName) }

// EnsureDir creates the config directory (mode 0700) if it does not exist.
func (s *Store) EnsureDir() error {
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("create config dir %s: %w", s.dir, err)
	}
	return nil
}

// --- config.yaml -----------------------------------------------------------

// LoadConfig reads config.yaml. A missing file is not an error: the defaults
// are returned instead, so a fresh install works without any setup.
func (s *Store) LoadConfig() (*Config, error) {
	data, err := os.ReadFile(s.ConfigPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Default(), nil
		}
		return nil, fmt.Errorf("read %s: %w", s.ConfigPath(), err)
	}
	cfg := Default()
	if err := loadYAML(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", s.ConfigPath(), err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveConfig validates and writes config.yaml atomically.
func (s *Store) SaveConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("save config: nil config")
	}
	cp := cfg.Clone()
	cp.Normalize()
	if err := cp.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cp)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := s.EnsureDir(); err != nil {
		return err
	}
	return writeFileAtomic(s.ConfigPath(), data, plainFileMode)
}

// --- servers.yaml ----------------------------------------------------------

// LoadServers reads servers.yaml. A missing file is not an error and yields an
// empty set. Entries are normalized in memory; servers.yaml is only rewritten
// when explicitly saved (import/rm/select).
func (s *Store) LoadServers() (*ServerSet, error) {
	data, err := os.ReadFile(s.ServersPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewServerSet(), nil
		}
		return nil, fmt.Errorf("read %s: %w", s.ServersPath(), err)
	}
	ss := NewServerSet()
	if err := loadYAML(data, ss); err != nil {
		return nil, fmt.Errorf("%s: %w", s.ServersPath(), err)
	}
	if ss.Servers == nil {
		ss.Servers = []Server{}
	}
	for i := range ss.Servers {
		ss.Servers[i].Normalize()
	}
	if err := ss.Validate(); err != nil {
		return nil, err
	}
	return ss, nil
}

// SaveServers validates and writes servers.yaml (mode 0600) atomically.
func (s *Store) SaveServers(ss *ServerSet) error {
	if ss == nil {
		return errors.New("save servers: nil set")
	}
	for i := range ss.Servers {
		ss.Servers[i].Normalize()
	}
	if err := ss.Validate(); err != nil {
		return err
	}
	body, err := yaml.Marshal(ss)
	if err != nil {
		return fmt.Errorf("encode servers: %w", err)
	}
	data := append([]byte(serversHeader), body...)
	if err := s.EnsureDir(); err != nil {
		return err
	}
	// Rewrite with the restrictive mode even if the file already existed (and
	// fix up a file whose mode drifted).
	if err := writeFileAtomic(s.ServersPath(), data, secretFileMode); err != nil {
		return err
	}
	if err := os.Chmod(s.ServersPath(), secretFileMode); err != nil {
		return fmt.Errorf("chmod %s: %w", s.ServersPath(), err)
	}
	return nil
}

// AddServer loads servers.yaml, appends srv, and saves it back. overwrite
// replaces an existing entry with the same name.
func (s *Store) AddServer(srv Server, overwrite bool) (*ServerSet, error) {
	ss, err := s.LoadServers()
	if err != nil {
		return nil, err
	}
	if err := ss.Add(srv, overwrite); err != nil {
		return nil, err
	}
	if err := s.SaveServers(ss); err != nil {
		return nil, err
	}
	return ss, nil
}

// RemoveServer loads servers.yaml, deletes a named server, and saves it back.
// If the removed server was selected in config.yaml, the selection is cleared.
func (s *Store) RemoveServer(name string) (*Server, error) {
	ss, err := s.LoadServers()
	if err != nil {
		return nil, err
	}
	victim, ok := ss.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q (see the names in %s)", ErrNotFound, name, s.ServersPath())
	}
	removed := *victim
	if !ss.Remove(name) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err := s.SaveServers(ss); err != nil {
		return nil, err
	}

	if cfg, err := s.LoadConfig(); err == nil && cfg.SelectedServer != "" &&
		strings.EqualFold(cfg.SelectedServer, removed.Name) {
		cfg.SelectedServer = ""
		if err := s.SaveConfig(cfg); err != nil {
			return &removed, fmt.Errorf("cleared selection: %w", err)
		}
	}
	return &removed, nil
}

// SelectServer sets config.yaml's selected_server to a name present in
// servers.yaml, or to the "auto" sentinel (automatic failover over every
// server).
func (s *Store) SelectServer(name string) error {
	name = strings.TrimSpace(name)
	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	if strings.EqualFold(name, SelectedServerAuto) {
		ss, err := s.LoadServers()
		if err != nil {
			return err
		}
		if ss.Len() == 0 {
			return fmt.Errorf("%w: cannot select %q with no servers configured", ErrNotFound, SelectedServerAuto)
		}
		cfg.SelectedServer = SelectedServerAuto
		return s.SaveConfig(cfg)
	}
	ss, err := s.LoadServers()
	if err != nil {
		return err
	}
	srv, ok := ss.Get(name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	cfg.SelectedServer = srv.Name
	return s.SaveConfig(cfg)
}

// --- helpers ---------------------------------------------------------------

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so readers never observe a half-written config.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
