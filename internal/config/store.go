package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ll-lite/vender/internal/privatefs"
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
	// ProfilesFileName holds the profile registry: which profiles exist, the
	// subscription URL each came from, when it was last fetched, and which one
	// is active. It is written 0600 because a subscription URL often carries
	// a token.
	ProfilesFileName = "profiles.yaml"

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

// profilesHeader is prepended to profiles.yaml.
const profilesHeader = "# vender profile registry: which server groups exist, where each subscription\n" +
	"# came from, its captured DNS policy for the profile's node hostnames, and\n" +
	"# which profile `selected_server: auto` currently scopes to.\n" +
	"# Contains no server credentials, but a subscription URL may carry a token, so\n" +
	"# it is written with mode 0600. Managed by `vender import --url` / `vender profiles`.\n"

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

// ProfilesPath returns the path of profiles.yaml.
func (s *Store) ProfilesPath() string { return filepath.Join(s.dir, ProfilesFileName) }

// EnsureDir creates the config directory (mode 0700) if it does not exist.
func (s *Store) EnsureDir() error {
	if err := privatefs.EnsureDir(s.dir); err != nil {
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
	if err := privatefs.Protect(s.ServersPath()); err != nil {
		return nil, err
	}
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

// --- profiles.yaml ---------------------------------------------------------

// LoadProfiles reads profiles.yaml. A missing file is not an error and yields
// an empty registry (whose active profile is the built-in "manual" one, which
// is exactly what an existing installation behaves like).
func (s *Store) LoadProfiles() (*ProfileSet, error) {
	if err := privatefs.Protect(s.ProfilesPath()); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.ProfilesPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewProfileSet(), nil
		}
		return nil, fmt.Errorf("read %s: %w", s.ProfilesPath(), err)
	}
	ps := NewProfileSet()
	if err := loadYAML(data, ps); err != nil {
		return nil, fmt.Errorf("%s: %w", s.ProfilesPath(), err)
	}
	ps.Normalize()
	if err := ps.Validate(); err != nil {
		return nil, err
	}
	return ps, nil
}

// SaveProfiles normalizes and writes profiles.yaml (mode 0600, because a
// subscription URL can carry a token) atomically.
func (s *Store) SaveProfiles(ps *ProfileSet) error {
	if ps == nil {
		return errors.New("save profiles: nil set")
	}
	cp := *ps
	cp.Profiles = append([]Profile(nil), ps.Profiles...)
	cp.Normalize()
	if err := cp.Validate(); err != nil {
		return err
	}
	body, err := yaml.Marshal(&cp)
	if err != nil {
		return fmt.Errorf("encode profiles: %w", err)
	}
	data := append([]byte(profilesHeader), body...)
	if err := s.EnsureDir(); err != nil {
		return err
	}
	return writeFileAtomic(s.ProfilesPath(), data, secretFileMode)
}

// ImportStats summarises one profile import or refresh, so the CLI can report
// a per-node outcome rather than a bare count.
type ImportStats struct {
	// Added is the number of new entries appended.
	Added int
	// Updated is the number of existing entries replaced by a changed one (a
	// re-import of an unchanged entry is not counted here).
	Updated int
	// Unchanged is the number of entries that were already present verbatim.
	Unchanged int
	// Skipped is the nodes whose name is already taken by a server in another
	// profile (a --force re-run replaces them instead).
	Skipped []NameConflict
	// Removed is the names dropped by a refresh because the subscription no
	// longer publishes them.
	Removed []string
}

// UpsertProfileServers merges servers into one profile.
//
// A name that already exists *inside* the profile is updated in place (that is
// what re-importing a subscription means); a name that exists in a *different*
// profile is a conflict and is skipped unless force is set. Entries that are
// already present verbatim are left alone and counted as unchanged, so a
// repeated import does not churn servers.yaml.
func (ss *ServerSet) UpsertProfileServers(profile string, servers []Server, force bool) (stats ImportStats, err error) {
	profile = normalizeProfileName(profile)
	if profile == "" {
		profile = ProfileManual
	}
	tag := profileTag(profile)
	index := make(map[string]int, len(ss.Servers))
	for i := range ss.Servers {
		index[strings.ToLower(ss.Servers[i].Name)] = i
	}
	for _, srv := range servers {
		srv.Profile = tag
		srv.Normalize()
		if verr := srv.Validate(); verr != nil {
			return stats, verr
		}
		i, ok := index[strings.ToLower(srv.Name)]
		if !ok {
			ss.Servers = append(ss.Servers, srv)
			index[strings.ToLower(srv.Name)] = len(ss.Servers) - 1
			stats.Added++
			continue
		}
		if !force && !strings.EqualFold(ss.Servers[i].ProfileName(), profile) {
			// Belongs to another profile: leave it, and let the caller report
			// the collision (it is not silently dropped).
			stats.Skipped = append(stats.Skipped, NameConflict{Name: srv.Name, InProfile: ss.Servers[i].ProfileName()})
			continue
		}
		if serversEqual(ss.Servers[i], srv) {
			stats.Unchanged++
			continue
		}
		ss.Servers[i] = srv
		stats.Updated++
	}
	return stats, nil
}

// NameConflict is a subscription node whose name is already taken by a server
// in another profile.
type NameConflict struct {
	Name      string
	InProfile string
}

// RemoveServersExcept deletes the servers of a profile whose names are not in
// keep, returning the names removed. It is how `profiles update` drops nodes a
// subscription no longer publishes.
func (ss *ServerSet) RemoveServersExcept(profile string, keep []string) []string {
	profile = normalizeProfileName(profile)
	if profile == "" {
		profile = ProfileManual
	}
	keepSet := make(map[string]bool, len(keep))
	for _, k := range keep {
		keepSet[strings.ToLower(k)] = true
	}
	kept := make([]Server, 0, len(ss.Servers))
	var removed []string
	for i := range ss.Servers {
		s := ss.Servers[i]
		if strings.EqualFold(s.ProfileName(), profile) && !keepSet[strings.ToLower(s.Name)] {
			removed = append(removed, s.Name)
			continue
		}
		kept = append(kept, s)
	}
	ss.Servers = kept
	return removed
}

// ProfileImport adds servers under a profile, records its URL, fetch time and
// captured DNS policy, and persists both files. When replace is true the
// profile's previous servers are dropped first (a bulk subscription refresh);
// otherwise new and changed entries are merged in and nothing is removed.
//
// dns is the policy captured from the subscription. A nil dns leaves an
// existing policy alone, so a manual `profiles set-dns` override survives a
// refresh of a subscription that does not declare one; a non-nil dns replaces
// it (including with an empty policy, if the source stopped declaring one).
//
// It returns the stats and the (normalized) profile name that was used.
func (s *Store) ProfileImport(profile, url string, fetchedAt time.Time, servers []Server, dns *ProfileDNS, replace, force bool) (ImportStats, string, error) {
	profile = normalizeProfileName(profile)
	if profile == "" {
		profile = ProfileManual
	}
	if strings.EqualFold(profile, ProfileManual) {
		return ImportStats{}, profile, fmt.Errorf("a subscription cannot be imported into the built-in %q profile: choose a name with --profile", ProfileManual)
	}
	ss, err := s.LoadServers()
	if err != nil {
		return ImportStats{}, profile, err
	}
	ps, err := s.LoadProfiles()
	if err != nil {
		return ImportStats{}, profile, err
	}
	var stats ImportStats
	if replace {
		names := make([]string, 0, len(servers))
		for _, srv := range servers {
			names = append(names, srv.Name)
		}
		stats.Removed = ss.RemoveServersExcept(profile, names)
	}
	merged, err := ss.UpsertProfileServers(profile, servers, force)
	if err != nil {
		return ImportStats{}, profile, err
	}
	merged.Removed = stats.Removed
	stats = merged
	prof := ps.Upsert(Profile{Name: profile, URL: url, FetchedAt: fetchedAt})
	if prof == nil {
		return stats, profile, fmt.Errorf("profile name %q is not valid", profile)
	}
	if dns != nil {
		cp := dns.Clone()
		cp.Normalize()
		if cp.IsEmpty() {
			cp = nil
		}
		prof.DNS = cp
	}
	if err := s.SaveServers(ss); err != nil {
		return stats, profile, err
	}
	if err := s.SaveProfiles(ps); err != nil {
		return stats, profile, err
	}
	return stats, profile, nil
}

// SetProfileDNS replaces a profile's DNS policy in profiles.yaml, creating the
// registry entry when the profile is known only through servers.yaml. A nil
// policy clears it. It returns the canonical profile name.
func (s *Store) SetProfileDNS(name string, dns *ProfileDNS) (string, error) {
	ps, err := s.LoadProfiles()
	if err != nil {
		return "", err
	}
	ss, err := s.LoadServers()
	if err != nil {
		return "", err
	}
	canonical, ok := KnownProfileName(ss, ps, name)
	if !ok {
		return "", fmt.Errorf("%w: profile %q (see `vender profiles list`)", ErrNotFound, name)
	}
	if canonical == ProfileAll {
		return "", fmt.Errorf("refusing to set a DNS policy on %q: name a single profile", ProfileAll)
	}
	name, err = ps.SetDNS(canonical, dns)
	if err != nil {
		return "", err
	}
	if err := s.SaveProfiles(ps); err != nil {
		return "", err
	}
	return name, nil
}

// SelectProfile sets profiles.yaml's active profile: the pool
// `selected_server: auto` scopes to. The name is resolved against the known
// profiles (the built-in manual one, the registry, and the profile names
// carried by servers), and ProfileAll / "" means "every server". It returns
// the effective active name.
func (s *Store) SelectProfile(name string) (string, error) {
	ps, err := s.LoadProfiles()
	if err != nil {
		return "", err
	}
	ss, err := s.LoadServers()
	if err != nil {
		return "", err
	}
	canonical, ok := KnownProfileName(ss, ps, name)
	if !ok {
		return "", fmt.Errorf("%w: profile %q (see `vender profiles list`)", ErrNotFound, name)
	}
	switch {
	case canonical == ProfileAll:
		ps.Active = ProfileAll
	case strings.EqualFold(canonical, ProfileManual):
		ps.Active = ""
	default:
		ps.Active = canonical
	}
	if err := s.SaveProfiles(ps); err != nil {
		return "", err
	}
	return ps.ActiveName(), nil
}

// RemoveProfile deletes a profile: its servers, its registry entry, and the
// active selection if it was the active one. It refuses to touch the built-in
// manual profile, which is what guarantees `profiles rm` can never leave the
// user with no profile at all.
//
// If config.yaml selected a server that the removal deletes, the selection is
// switched to "auto" so the daemon can still start.
func (s *Store) RemoveProfile(name string) (removed int, err error) {
	canonical := NormalizeProfileName(name)
	if canonical == "" {
		return 0, fmt.Errorf("refusing to remove every server: name a single profile")
	}
	if strings.EqualFold(canonical, ProfileManual) {
		return 0, fmt.Errorf("the built-in %q profile cannot be removed: it holds every server imported by hand", ProfileManual)
	}
	ss, err := s.LoadServers()
	if err != nil {
		return 0, err
	}
	ps, err := s.LoadProfiles()
	if err != nil {
		return 0, err
	}
	if _, known := KnownProfileName(ss, ps, canonical); !known {
		return 0, fmt.Errorf("%w: profile %q (see `vender profiles list`)", ErrNotFound, name)
	}
	removedNames := map[string]bool{}
	for i := range ss.Servers {
		if strings.EqualFold(ss.Servers[i].ProfileName(), canonical) {
			removedNames[strings.ToLower(ss.Servers[i].Name)] = true
		}
	}
	n, err := ss.RemoveProfile(canonical)
	if err != nil {
		return 0, err
	}
	ps.Remove(canonical)
	if strings.EqualFold(ps.ActiveName(), canonical) {
		ps.Active = ""
	}
	if err := s.SaveServers(ss); err != nil {
		return 0, err
	}
	if err := s.SaveProfiles(ps); err != nil {
		return 0, err
	}
	// Keep the selection sane: a selected server that no longer exists would
	// make the daemon refuse to start.
	if cfg, cerr := s.LoadConfig(); cerr == nil && cfg.SelectedServer != "" && !cfg.IsAutoServer() {
		if removedNames[strings.ToLower(cfg.SelectedServer)] {
			cfg.SelectedServer = SelectedServerAuto
			if serr := s.SaveConfig(cfg); serr != nil {
				return n, fmt.Errorf("profile removed, but clearing the selection failed: %w", serr)
			}
		}
	}
	return n, nil
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
