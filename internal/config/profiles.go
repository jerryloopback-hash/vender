package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Profiles.
//
// A *profile* is a named group of servers in servers.yaml: the servers carry
// `profile: <name>`, and profiles.yaml is only the small registry that records
// where the group came from (a subscription URL) and when it was last fetched.
//
// Why membership lives on the server and metadata in a separate file:
//
//   - servers.yaml alone stays sufficient to know which servers belong
//     together, so a user who hand-edits it (or copies it between machines)
//     cannot end up with a profile that references servers it no longer has;
//   - profiles.yaml is tiny and safe to lose: it holds a URL, a timestamp and
//     the name of the active profile, and can be rebuilt by re-importing.
//
// The one built-in profile is ProfileManual: every server that did not come
// from a subscription. It always exists, which is what makes `active` default
// to it and keeps a pre-M7 servers.yaml behaving exactly as before (auto mode
// over every server, because every server is manual).

const (
	// ProfileManual is the implicit profile of hand-imported servers. It is
	// never stored in profiles.yaml and cannot be removed.
	ProfileManual = "manual"
	// ProfileAll is accepted by the CLI as "every server, whatever profile it
	// is in". It is not a stored profile; it is the empty active value.
	ProfileAll = "all"
)

// Profile records where a named group of servers came from.
type Profile struct {
	Name string `yaml:"name"`
	// URL is the subscription this profile was imported from. It is empty for
	// a profile that was only ever populated from share links typed by hand.
	URL string `yaml:"url,omitempty"`
	// FetchedAt is when the subscription was last successfully fetched. The
	// zero value means "never fetched" (the profile was created by hand).
	FetchedAt time.Time `yaml:"fetched_at,omitempty"`
}

// ProfileSet is the contents of profiles.yaml.
type ProfileSet struct {
	// Active names the profile that `selected_server: auto` scopes to. An
	// empty value means ProfileManual, so an absent file behaves like an
	// existing installation (auto over every server, because they are all
	// manual).
	Active   string    `yaml:"active,omitempty"`
	Profiles []Profile `yaml:"profiles"`
}

// NewProfileSet returns an empty registry (never nil, so YAML emits
// `profiles: []`).
func NewProfileSet() *ProfileSet {
	return &ProfileSet{Profiles: []Profile{}}
}

// Normalize trims values, drops nameless/duplicate entries and canonicalises
// the active name. It is idempotent and is applied on load and before save.
func (ps *ProfileSet) Normalize() {
	if ps == nil {
		return
	}
	ps.Active = strings.TrimSpace(ps.Active)
	out := make([]Profile, 0, len(ps.Profiles))
	seen := make(map[string]bool, len(ps.Profiles))
	for _, p := range ps.Profiles {
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" || strings.EqualFold(p.Name, ProfileManual) {
			// `manual` is built in and must not be stored.
			continue
		}
		key := strings.ToLower(p.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		p.URL = strings.TrimSpace(p.URL)
		out = append(out, p)
	}
	ps.Profiles = out
	switch {
	case ps.Active == "" || strings.EqualFold(ps.Active, ProfileManual):
		// manual is the implicit default, so it is stored as absent.
		ps.Active = ""
	case strings.EqualFold(ps.Active, ProfileAll):
		ps.Active = ProfileAll
	}
}

// ActiveName returns the effective active profile: the configured name, or
// ProfileManual when nothing is set. It may be ProfileAll, meaning "every
// server".
func (ps *ProfileSet) ActiveName() string {
	if ps == nil {
		return ProfileManual
	}
	if v := strings.TrimSpace(ps.Active); v != "" {
		return v
	}
	return ProfileManual
}

// Get returns a named profile from the registry. ProfileManual is not in the
// registry but is always "known"; use KnownProfile for that question.
func (ps *ProfileSet) Get(name string) (*Profile, bool) {
	if ps == nil {
		return nil, false
	}
	for i := range ps.Profiles {
		if strings.EqualFold(ps.Profiles[i].Name, strings.TrimSpace(name)) {
			return &ps.Profiles[i], true
		}
	}
	return nil, false
}

// Upsert adds or replaces a profile's metadata, preserving its position when
// it already exists.
func (ps *ProfileSet) Upsert(p Profile) *Profile {
	if ps == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(p.Name), ProfileManual) || strings.TrimSpace(p.Name) == "" {
		return nil
	}
	if existing, ok := ps.Get(p.Name); ok {
		existing.Name = strings.TrimSpace(p.Name)
		if p.URL != "" {
			existing.URL = strings.TrimSpace(p.URL)
		}
		if !p.FetchedAt.IsZero() {
			existing.FetchedAt = p.FetchedAt
		}
		return existing
	}
	p.Name = strings.TrimSpace(p.Name)
	p.URL = strings.TrimSpace(p.URL)
	ps.Profiles = append(ps.Profiles, p)
	return &ps.Profiles[len(ps.Profiles)-1]
}

// Remove deletes a profile from the registry and reports whether it existed.
func (ps *ProfileSet) Remove(name string) bool {
	if ps == nil {
		return false
	}
	for i := range ps.Profiles {
		if strings.EqualFold(ps.Profiles[i].Name, strings.TrimSpace(name)) {
			ps.Profiles = append(ps.Profiles[:i], ps.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

// RegisteredNames returns the registry's profile names, in file order.
func (ps *ProfileSet) RegisteredNames() []string {
	if ps == nil {
		return nil
	}
	out := make([]string, 0, len(ps.Profiles))
	for i := range ps.Profiles {
		out = append(out, ps.Profiles[i].Name)
	}
	return out
}

// KnownProfiles returns every profile vender knows about: the built-in manual
// profile, the names in the registry, and the profile names actually carried
// by a server. The union matters because either half can exist alone: a
// hand-edited servers.yaml can name a profile the registry has never heard of,
// and an imported profile can have all its servers removed by hand.
func KnownProfiles(ss *ServerSet, ps *ProfileSet) []string {
	seen := make(map[string]string)
	var order []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = name
		order = append(order, name)
	}
	add(ProfileManual)
	for _, n := range ps.RegisteredNames() {
		add(n)
	}
	if ss != nil {
		for _, n := range ss.ProfileNames() {
			add(n)
		}
	}
	return order
}

// KnownProfileName resolves name (case-insensitively) to its canonical
// spelling among the known profiles, or reports false.
func KnownProfileName(ss *ServerSet, ps *ProfileSet, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, ProfileAll) {
		return ProfileAll, true
	}
	for _, known := range KnownProfiles(ss, ps) {
		if strings.EqualFold(known, name) {
			return known, true
		}
	}
	return "", false
}

// NormalizeProfileName canonicalises a user-supplied profile name, accepting
// "all" (and the empty string) as the "every server" pseudo-profile.
func NormalizeProfileName(name string) string { return normalizeProfileName(name) }

func normalizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, ProfileAll) {
		return ""
	}
	if strings.EqualFold(name, ProfileManual) {
		return ProfileManual
	}
	return name
}

// profileTag is what is written into Server.Profile: every pseudo-profile
// ("", "all", "manual") is stored as absent, because the manual profile is
// implicit and never appears in servers.yaml.
func profileTag(name string) string {
	name = normalizeProfileName(name)
	if strings.EqualFold(name, ProfileManual) {
		return ""
	}
	return name
}

// --- membership, read from servers.yaml -------------------------------------

// InProfile returns the servers belonging to a profile, in servers.yaml order.
// An empty name (or ProfileAll) means every server, which is the pre-M7
// behaviour and what makes an absent profiles.yaml a no-op.
func (ss *ServerSet) InProfile(name string) []Server {
	if ss == nil {
		return nil
	}
	name = normalizeProfileName(name)
	if name == "" {
		out := make([]Server, len(ss.Servers))
		copy(out, ss.Servers)
		return out
	}
	var out []Server
	for i := range ss.Servers {
		if strings.EqualFold(ss.Servers[i].ProfileName(), name) {
			out = append(out, ss.Servers[i])
		}
	}
	return out
}

// CountInProfile counts the servers of a profile ("" = all).
func (ss *ServerSet) CountInProfile(name string) int {
	if ss == nil {
		return 0
	}
	name = normalizeProfileName(name)
	if name == "" {
		return len(ss.Servers)
	}
	n := 0
	for i := range ss.Servers {
		if strings.EqualFold(ss.Servers[i].ProfileName(), name) {
			n++
		}
	}
	return n
}

// ProfileNames returns the distinct profile names carried by the servers, in
// first-appearance order.
func (ss *ServerSet) ProfileNames() []string {
	if ss == nil {
		return nil
	}
	seen := make(map[string]bool, len(ss.Servers))
	var out []string
	for i := range ss.Servers {
		name := ss.Servers[i].ProfileName()
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	return out
}

// RemoveProfile deletes every server of a profile and reports how many were
// removed. The built-in manual profile cannot be removed.
func (ss *ServerSet) RemoveProfile(name string) (int, error) {
	if ss == nil {
		return 0, nil
	}
	name = normalizeProfileName(name)
	if name == "" {
		return 0, fmt.Errorf("refusing to remove every server: name a profile")
	}
	if strings.EqualFold(name, ProfileManual) {
		return 0, fmt.Errorf("the built-in %q profile cannot be removed (it holds servers imported by hand)", ProfileManual)
	}
	kept := make([]Server, 0, len(ss.Servers))
	removed := 0
	for i := range ss.Servers {
		if strings.EqualFold(ss.Servers[i].ProfileName(), name) {
			removed++
			continue
		}
		kept = append(kept, ss.Servers[i])
	}
	ss.Servers = kept
	return removed, nil
}

// ProfileName is the profile a server belongs to, defaulting to manual for an
// unnormalized entry (so a raw servers.yaml read behaves like a normalized
// one).
func (s *Server) ProfileName() string {
	if p := strings.TrimSpace(s.Profile); p != "" {
		return p
	}
	return ProfileManual
}

// serversEqual reports whether two normalized servers describe the same node.
// It is what lets a repeated import leave an unchanged entry alone.
func serversEqual(a, b Server) bool {
	return reflect.DeepEqual(a, b)
}

// Validate rejects a registry that cannot be represented: a nameless entry, a
// duplicate, or a reserved name. The active profile is *not* required to exist
// — a registry can name a profile whose servers were removed by hand, and the
// effective active profile falls back to manual (see ActiveName/Effective).
func (ps *ProfileSet) Validate() error {
	if ps == nil {
		return nil
	}
	var problems []string
	seen := make(map[string]bool, len(ps.Profiles))
	for i, p := range ps.Profiles {
		name := strings.TrimSpace(p.Name)
		switch {
		case name == "":
			problems = append(problems, fmt.Sprintf("profile #%d has no name", i+1))
			continue
		case strings.EqualFold(name, ProfileManual):
			problems = append(problems, fmt.Sprintf("%q is the built-in profile name and cannot be used", name))
			continue
		case strings.EqualFold(name, ProfileAll):
			problems = append(problems, fmt.Sprintf("%q is a reserved name meaning \"every server\" and cannot be a profile", name))
			continue
		case strings.ContainsAny(name, "\n\r\t"):
			problems = append(problems, fmt.Sprintf("profile name %q must not contain control characters", name))
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			problems = append(problems, fmt.Sprintf("duplicate profile name %q", name))
			continue
		}
		seen[key] = true
	}
	if len(problems) > 0 {
		return &ValidationError{Subject: ProfilesFileName, Problems: problems}
	}
	return nil
}
