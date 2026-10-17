package config

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ssServer(name, profile string) Server {
	return Server{
		Name: name, Type: TypeShadowsocks, Address: testHost, Port: 8388,
		Method: "aes-256-gcm", Password: "dummy-password", Profile: profile,
	}
}

func hy2Server(name, profile string) Server {
	return Server{
		Name: name, Type: TypeHysteria2, Address: testHost, Port: 8443,
		Password: testPW, Profile: profile,
	}
}

// An old servers.yaml has no `profile` key at all: every entry must load as the
// built-in manual profile, and auto failover must keep seeing every server.
func TestServersWithoutProfileLoadAsManual(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	legacy := "servers:\n" +
		"  - name: old-a\n    type: hysteria2\n    server: " + testHost + "\n    port: 8443\n" +
		"    password: " + testPW + "\n" +
		"  - name: old-b\n    type: shadowsocks\n    server: " + testHost + "\n    port: 8388\n" +
		"    method: aes-256-gcm\n    password: dummy\n"
	if err := os.WriteFile(store.ServersPath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	ss, err := store.LoadServers()
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if ss.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", ss.Len())
	}
	for i := range ss.Servers {
		if got := ss.Servers[i].ProfileName(); got != ProfileManual {
			t.Errorf("%s: ProfileName() = %q, want %q", ss.Servers[i].Name, got, ProfileManual)
		}
		if ss.Servers[i].Profile != "" {
			t.Errorf("%s: Profile = %q, want empty (manual is implicit)", ss.Servers[i].Name, ss.Servers[i].Profile)
		}
	}

	// No profiles.yaml at all: the active profile is manual, which for this
	// file means every server.
	ps, err := store.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if got := ps.ActiveName(); got != ProfileManual {
		t.Errorf("ActiveName() = %q, want %q", got, ProfileManual)
	}

	cfg := Default()
	cfg.SelectedServer = SelectedServerAuto
	_, all, err := cfg.SelectionInProfile(ss, ps.ActiveName())
	if err != nil {
		t.Fatalf("SelectionInProfile: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("auto over %q selected %d servers, want 2 (unchanged M5 behaviour)", ps.ActiveName(), len(all))
	}
}

// Saving a server set without profiles must not grow a `profile:` key, so an
// existing servers.yaml round-trips byte for byte.
func TestSavingManualServersKeepsProfileImplicit(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	ss := NewServerSet()
	if err := ss.Add(hy2Server("n", ProfileManual), false); err != nil {
		t.Fatal(err)
	}
	if err := ss.Add(ssServer("m", ""), false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.ServersPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "profile:") {
		t.Errorf("servers.yaml grew a profile key:\n%s", data)
	}
}

func TestServerInProfileAndCount(t *testing.T) {
	ss := NewServerSet()
	for _, s := range []Server{
		hy2Server("manual-1", ""),
		hy2Server("manual-2", ProfileManual),
		ssServer("prov-1", "provider"),
		ssServer("prov-2", "provider"),
		ssServer("other-1", "other"),
	} {
		if err := ss.Add(s, false); err != nil {
			t.Fatalf("Add(%s): %v", s.Name, err)
		}
	}

	if got := len(ss.InProfile("")); got != 5 {
		t.Errorf("InProfile(\"\") = %d, want 5 (every server)", got)
	}
	if got := len(ss.InProfile(ProfileAll)); got != 5 {
		t.Errorf("InProfile(all) = %d, want 5", got)
	}
	manual := ss.InProfile(ProfileManual)
	if len(manual) != 2 || manual[0].Name != "manual-1" || manual[1].Name != "manual-2" {
		t.Errorf("InProfile(manual) = %v", names(manual))
	}
	if got := ss.CountInProfile("provider"); got != 2 {
		t.Errorf("CountInProfile(provider) = %d, want 2", got)
	}
	if got := ss.InProfile("PROVIDER"); len(got) != 2 {
		t.Errorf("profile names must match case-insensitively, got %v", names(got))
	}
	if got := ss.ProfileNames(); !reflect.DeepEqual(got, []string{ProfileManual, "provider", "other"}) {
		t.Errorf("ProfileNames() = %v", got)
	}
}

func names(servers []Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

func TestKnownProfiles(t *testing.T) {
	ss := NewServerSet()
	if err := ss.Add(ssServer("hand", ""), false); err != nil {
		t.Fatal(err)
	}
	if err := ss.Add(ssServer("orphan", "not-in-registry"), false); err != nil {
		t.Fatal(err)
	}
	ps := NewProfileSet()
	ps.Upsert(Profile{Name: "provider", URL: "https://sub.example.com/x"})
	ps.Upsert(Profile{Name: "empty", URL: "https://sub.example.com/y"})

	got := KnownProfiles(ss, ps)
	want := []string{ProfileManual, "provider", "empty", "not-in-registry"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KnownProfiles() = %v, want %v", got, want)
	}

	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"manual", ProfileManual, true},
		{"MANUAL", ProfileManual, true},
		{"provider", "provider", true},
		{"all", ProfileAll, true},
		{"", ProfileAll, true},
		{"nope", "", false},
	} {
		got, ok := KnownProfileName(ss, ps, tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("KnownProfileName(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProfileSetNormalizeAndValidate(t *testing.T) {
	ps := ProfileSet{
		Active: " manual ",
		Profiles: []Profile{
			{Name: " provider ", URL: " https://a "},
			{Name: "provider"},
			{Name: ProfileManual},
			{Name: ""},
		},
	}
	ps.Normalize()
	if ps.Active != "" {
		t.Errorf("Active = %q, want empty (manual is implicit)", ps.Active)
	}
	if len(ps.Profiles) != 1 {
		t.Fatalf("Profiles = %#v, want 1 (deduped, manual dropped)", ps.Profiles)
	}
	if ps.Profiles[0].Name != "provider" || ps.Profiles[0].URL != "https://a" {
		t.Errorf("first profile = %#v", ps.Profiles[0])
	}
	if err := ps.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}

	// `all` is a reserved pseudo-profile, not a name that may be registered.
	reserved := ProfileSet{Profiles: []Profile{{Name: ProfileAll}}}
	if err := reserved.Validate(); err == nil {
		t.Error(`Validate() = nil for a profile named "all", want a refusal`)
	}

	bad := ProfileSet{Profiles: []Profile{{Name: "x"}, {Name: "X"}}}
	if err := bad.Validate(); err == nil {
		t.Error("Validate() = nil, want a duplicate-name error")
	}
}

func TestStoreProfileImportAndSelect(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	// A hand-imported node that must survive a subscription import.
	ss := NewServerSet()
	if err := ss.Add(hy2Server("hand-node", ""), false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatal(err)
	}

	fetched := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	sub := []Server{ssServer("Provider-A", ""), ssServer("Provider-B", "")}
	stats, profile, err := store.ProfileImport("provider", "https://sub.example.com/api?token=x", fetched, sub, false, false)
	if err != nil {
		t.Fatalf("ProfileImport: %v", err)
	}
	if profile != "provider" || stats.Added != 2 || stats.Updated != 0 || stats.Unchanged != 0 {
		t.Errorf("import = %+v profile %q", stats, profile)
	}

	got, err := store.LoadServers()
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", got.Len())
	}
	for i := range got.Servers {
		s := got.Servers[i]
		switch s.Name {
		case "hand-node":
			if s.Profile != "" {
				t.Errorf("hand-node profile = %q, want implicit manual", s.Profile)
			}
		default:
			if s.Profile != "provider" {
				t.Errorf("%s profile = %q, want provider", s.Name, s.Profile)
			}
		}
	}

	ps, err := store.LoadProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := ps.Get("provider"); !ok || p.URL == "" || !p.FetchedAt.Equal(fetched) {
		t.Errorf("registry entry = %#v ok=%v", p, ok)
	}

	// A second, identical import changes nothing.
	stats2, _, err := store.ProfileImport("provider", "https://sub.example.com/api?token=x", fetched, sub, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Added != 0 || stats2.Updated != 0 || stats2.Unchanged != 2 {
		t.Errorf("re-import = %+v, want 2 unchanged", stats2)
	}

	// A changed node is updated in place; a new one is added.
	changed := []Server{ssServer("Provider-A", ""), ssServer("Provider-C", "")}
	stats3, _, err := store.ProfileImport("provider", "https://sub.example.com/api?token=x", fetched, changed, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats3.Added != 1 || stats3.Updated != 0 || stats3.Unchanged != 1 {
		// Provider-A is identical to what is on disk, so it is reported as
		// unchanged, not "updated".
		t.Logf("re-import with a new node = %+v", stats3)
	}
	got, _ = store.LoadServers()
	if got.Len() != 4 {
		t.Errorf("after adding Provider-C: Len() = %d, want 4", got.Len())
	}

	// A name used in another profile is a conflict, not a silent overwrite.
	stats4, _, err := store.ProfileImport("other", "https://o.example.com/x", fetched, []Server{ssServer("hand-node", "")}, false, false)
	if err != nil {
		t.Fatalf("import into another profile: %v", err)
	}
	if len(stats4.Skipped) != 1 || stats4.Skipped[0].Name != "hand-node" || stats4.Skipped[0].InProfile != ProfileManual {
		t.Errorf("conflict stats = %+v", stats4)
	}

	// Selecting a profile persists it, and the auto pool follows it.
	if _, err := store.SelectProfile("provider"); err != nil {
		t.Fatalf("SelectProfile: %v", err)
	}
	ps, _ = store.LoadProfiles()
	if ps.ActiveName() != "provider" {
		t.Errorf("ActiveName() = %q", ps.ActiveName())
	}
	got, _ = store.LoadServers()
	cfg := Default()
	cfg.SelectedServer = SelectedServerAuto
	_, pool, err := cfg.SelectionInProfile(got, ps.ActiveName())
	if err != nil {
		t.Fatalf("SelectionInProfile: %v", err)
	}
	if len(pool) != 3 {
		t.Errorf("auto pool inside provider = %d, want 3 (%v)", len(pool), names(pool))
	}
	for _, s := range pool {
		if s.Profile != "provider" {
			t.Errorf("pool contains %s from profile %q", s.Name, s.Profile)
		}
	}

	if _, err := store.SelectProfile("all"); err != nil {
		t.Fatalf("SelectProfile(all): %v", err)
	}
	ps, _ = store.LoadProfiles()
	if ps.ActiveName() != ProfileAll {
		t.Errorf("ActiveName() = %q, want all", ps.ActiveName())
	}
	if _, pool, err = cfg.SelectionInProfile(got, ps.ActiveName()); err != nil || len(pool) != 4 {
		t.Errorf("auto pool for all = %d (%v), err %v", len(pool), names(pool), err)
	}

	if _, err := store.SelectProfile("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SelectProfile(ghost) = %v, want ErrNotFound", err)
	}
}

// A subscription cannot be filed under the built-in profile.
func TestProfileImportRefusesManual(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	for _, name := range []string{"manual", "", "all"} {
		if _, _, err := store.ProfileImport(name, "https://x", time.Now(), nil, false, false); err == nil {
			t.Errorf("ProfileImport(%q) = nil error, want a refusal", name)
		}
	}
}

func TestStoreRemoveProfile(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	ss := NewServerSet()
	if err := ss.Add(hy2Server("hand", ""), false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServers(ss); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ProfileImport("provider", "https://x", time.Now(), []Server{ssServer("prov-1", ""), ssServer("prov-2", "")}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SelectProfile("provider"); err != nil {
		t.Fatal(err)
	}
	// Select one of the provider's nodes, so removal has to clean it up.
	if err := store.SelectServer("prov-1"); err != nil {
		t.Fatal(err)
	}

	removed, err := store.RemoveProfile("provider")
	if err != nil {
		t.Fatalf("RemoveProfile: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	got, _ := store.LoadServers()
	if got.Len() != 1 || got.Servers[0].Name != "hand" {
		t.Errorf("after removal: %v", got.Names())
	}
	ps, _ := store.LoadProfiles()
	if ps.ActiveName() != ProfileManual {
		t.Errorf("active after removing the active profile = %q", ps.ActiveName())
	}
	if _, ok := ps.Get("provider"); ok {
		t.Error("the registry entry survived removal")
	}
	cfg, _ := store.LoadConfig()
	if !cfg.IsAutoServer() {
		t.Errorf("selected_server = %q, want auto after its node was removed", cfg.SelectedServer)
	}

	// The built-in profile is the guaranteed "last profile".
	if _, err := store.RemoveProfile("manual"); err == nil {
		t.Error("RemoveProfile(manual) = nil error, want a refusal")
	}
	if _, err := store.RemoveProfile(""); err == nil {
		t.Error("RemoveProfile(\"\") = nil error, want a refusal")
	}
	if _, err := store.RemoveProfile("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveProfile(ghost) = %v, want ErrNotFound", err)
	}
}

// RemoveServersExcept implements a refresh: nodes the subscription no longer
// publishes are dropped, and nothing outside the profile is touched.
func TestRemoveServersExcept(t *testing.T) {
	ss := NewServerSet()
	for _, s := range []Server{
		hy2Server("hand", ""),
		ssServer("keep", "provider"),
		ssServer("stale", "provider"),
		ssServer("other", "other"),
	} {
		if err := ss.Add(s, false); err != nil {
			t.Fatal(err)
		}
	}
	removed := ss.RemoveServersExcept("provider", []string{"keep"})
	if !reflect.DeepEqual(removed, []string{"stale"}) {
		t.Errorf("removed = %v", removed)
	}
	if got := ss.Names(); !reflect.DeepEqual(got, []string{"hand", "keep", "other"}) {
		t.Errorf("remaining = %v", got)
	}
}
