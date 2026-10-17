package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseResolverAddress(t *testing.T) {
	cases := []struct {
		in      string
		want    ResolverAddress
		wantErr bool
	}{
		{in: "tcp://dns.example:8080", want: ResolverAddress{Type: "tcp", Host: "dns.example", Port: 8080}},
		{in: "udp://1.1.1.1:5353", want: ResolverAddress{Type: "udp", Host: "1.1.1.1", Port: 5353}},
		{in: "tls://dns.example", want: ResolverAddress{Type: "tls", Host: "dns.example", Port: 853}},
		{in: "https://dns.example/dns-query", want: ResolverAddress{Type: "https", Host: "dns.example", Port: 443, Path: "/dns-query"}},
		{in: "https://dns.example:8443/q", want: ResolverAddress{Type: "https", Host: "dns.example", Port: 8443, Path: "/q"}},
		{in: "https://dns.example", want: ResolverAddress{Type: "https", Host: "dns.example", Port: 443}},
		// A bare address is UDP/53, which is what a nameserver-policy without a
		// scheme means.
		{in: "8.8.8.8", want: ResolverAddress{Type: "udp", Host: "8.8.8.8", Port: 53}},
		{in: "dns.example:5353", want: ResolverAddress{Type: "udp", Host: "dns.example", Port: 5353}},
		{in: "tcp://[2001:db8::1]:53", want: ResolverAddress{Type: "tcp", Host: "2001:db8::1", Port: 53}},
		{in: " [ tcp://dns.example:8080 ] ", wantErr: true},
		{in: "", wantErr: true},
		{in: "dhcp://eth0", wantErr: true},
		{in: "tcp://dns.example:0", wantErr: true},
		{in: "tcp://dns.example:70000", wantErr: true},
		{in: "tcp://dns.example#frag", wantErr: true},
		{in: "udp://user@dns.example", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseResolverAddress(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseResolverAddress(%q) = %+v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseResolverAddress(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseResolverAddress(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeDNSPattern(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"+.v51124-6.qpon", "v51124-6.qpon", true},
		{"*.v51124-6.qpon", "v51124-6.qpon", true},
		{".v51124-6.qpon", "v51124-6.qpon", true},
		{"v51124-6.qpon", "v51124-6.qpon", true},
		{"+.V51124-6.QPON.", "v51124-6.qpon", true},
		{"geosite:cn", "", false},
		{"rule-set:cn", "", false},
		{"", "", false},
		{"  ", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeDNSPattern(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeDNSPattern(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProfileDNSMatch(t *testing.T) {
	dns := &ProfileDNS{NamespacePolicy: []DNSNamespacePolicy{
		{Domain: "qpon", Server: "tcp://broad.example:53"},
		{Domain: "v51124-6.qpon", Server: "tcp://dns.example:8080"},
		{Domain: "special.v51124-6.qpon", Server: "udp://203.0.113.9:53"},
	}}
	dns.Normalize()
	cases := map[string]string{
		"v51124-6.qpon":                          "tcp://dns.example:8080",
		"1.k.o.5.hk01-ae5.entry.v51124-6.qpon":   "tcp://dns.example:8080",
		"special.v51124-6.qpon":                  "udp://203.0.113.9:53",
		"deep.special.v51124-6.qpon":             "udp://203.0.113.9:53",
		"other.qpon":                             "tcp://broad.example:53",
		"notqpon":                                "",
		"example.net":                            "",
		"":                                       "",
		"n.k.h.y.l.entry.v51124-6.qpon.example.": "",
	}
	for host, want := range cases {
		if got := dns.Match(host); got != want {
			t.Errorf("Match(%q) = %q, want %q", host, got, want)
		}
	}
	var nilDNS *ProfileDNS
	if got := nilDNS.Match("anything"); got != "" {
		t.Errorf("nil policy Match = %q, want empty", got)
	}
}

func TestProfileDNSNormalizeAndValidate(t *testing.T) {
	dns := &ProfileDNS{
		NamespacePolicy: []DNSNamespacePolicy{
			{Domain: "+.example.com", Server: " tcp://a.example:8080 "},
			{Domain: "+.example.com", Server: "udp://dup:53"}, // duplicate: first wins
			{Domain: "geosite:cn", Server: "tcp://x:53"},      // dropped: not a domain
			{Domain: "*.b.example", Server: ""},               // dropped: no resolver
		},
		Nameservers: []string{"119.29.29.29", "119.29.29.29", "  "},
	}
	dns.Normalize()
	if len(dns.NamespacePolicy) != 1 {
		t.Fatalf("Normalize kept %d entries, want 1: %+v", len(dns.NamespacePolicy), dns.NamespacePolicy)
	}
	if dns.NamespacePolicy[0] != (DNSNamespacePolicy{Domain: "example.com", Server: "tcp://a.example:8080"}) {
		t.Errorf("normalized entry = %+v", dns.NamespacePolicy[0])
	}
	if len(dns.Nameservers) != 1 {
		t.Errorf("Normalize kept %d nameservers, want 1", len(dns.Nameservers))
	}
	if err := dns.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// A policy that normalizes to nothing collapses to nil so it is not
	// written out.
	empty := &ProfileDNS{NamespacePolicy: []DNSNamespacePolicy{{Domain: "geosite:cn", Server: "x"}}}
	empty.Normalize()
	if !empty.IsEmpty() {
		t.Errorf("a policy with no usable entry is not empty: %+v", empty)
	}

	bad := &ProfileDNS{NamespacePolicy: []DNSNamespacePolicy{{Domain: "example.com", Server: "dhcp://eth0"}}}
	if err := bad.Validate(); err == nil {
		t.Error("Validate accepted an unsupported resolver scheme")
	}
	unnormalized := &ProfileDNS{NamespacePolicy: []DNSNamespacePolicy{{Domain: "+.example.com", Server: "tcp://a:53"}}}
	if err := unnormalized.Validate(); err == nil {
		t.Error("Validate accepted a non-normalized domain")
	}
	unnormalized2 := &ProfileDNS{NamespacePolicy: []DNSNamespacePolicy{{Domain: "*.example.com", Server: "tcp://a:53"}}}
	if err := unnormalized2.Validate(); err == nil {
		t.Error("Validate accepted a *.example.com pattern that was not normalized")
	}
}

// The policy must survive a write/read of profiles.yaml, both its rules and
// its audit-only metadata, and an existing profiles.yaml without a `dns:` key
// must keep loading (and re-saving) without one.
func TestProfileDNSRoundTrip(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	ps := NewProfileSet()
	ps.Active = "provider"
	dns := &ProfileDNS{
		NamespacePolicy: []DNSNamespacePolicy{{Domain: "v51124-6.qpon", Server: "tcp://dns.example:8080"}},
		Nameservers:     []string{"119.29.29.29"},
		Fallback:        []string{"8.8.8.8"},
		Raw:             "nameserver-policy:\n  +.v51124-6.qpon: tcp://dns.example:8080\n",
	}
	if _, err := ps.SetDNS("provider", dns); err != nil {
		t.Fatalf("SetDNS: %v", err)
	}
	if err := store.SaveProfiles(ps); err != nil {
		t.Fatalf("SaveProfiles: %v", err)
	}

	raw, err := os.ReadFile(store.ProfilesPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"namespace_policy:", "v51124-6.qpon", "tcp://dns.example:8080", "nameservers:"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("profiles.yaml is missing %q:\n%s", want, raw)
		}
	}

	got, err := store.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	p, ok := got.Get("provider")
	if !ok {
		t.Fatal("profile provider is missing after a round trip")
	}
	if p.DNS == nil || len(p.DNS.NamespacePolicy) != 1 ||
		p.DNS.NamespacePolicy[0].Server != "tcp://dns.example:8080" {
		t.Fatalf("DNS policy did not survive the round trip: %+v", p.DNS)
	}
	if p.DNS.Raw == "" || len(p.DNS.Nameservers) != 1 || len(p.DNS.Fallback) != 1 {
		t.Errorf("captured metadata did not survive: %+v", p.DNS)
	}
	if m := got.DNSMap(); m["provider"] == nil {
		t.Error("DNSMap does not carry the profile policy")
	}

	// A legacy file without a dns key keeps round-tripping without one.
	legacy := NewProfileSet()
	legacy.Active = "provider"
	legacy.Profiles = []Profile{{Name: "provider", URL: "https://x", FetchedAt: time.Unix(1, 0)}}
	if err := store.SaveProfiles(legacy); err != nil {
		t.Fatalf("SaveProfiles (legacy): %v", err)
	}
	raw, _ = os.ReadFile(store.ProfilesPath())
	if strings.Contains(string(raw), "dns:") {
		t.Errorf("a profile without a policy wrote a dns key:\n%s", raw)
	}
	if got, err := store.LoadProfiles(); err != nil {
		t.Fatalf("LoadProfiles (legacy): %v", err)
	} else if _, ok := got.Get("provider"); !ok {
		t.Error("a legacy profile registry did not load")
	}
}

// ProfileImport stores the captured policy; a later import with no policy (a
// link list, or a subscription that dropped it) must not erase it, so a manual
// override survives a refresh.
func TestProfileImportDNSIsSticky(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	if err := store.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	dns := &ProfileDNS{NamespacePolicy: []DNSNamespacePolicy{{Domain: "v51124-6.qpon", Server: "tcp://dns.example:8080"}}}
	if _, _, err := store.ProfileImport("provider", "https://x", time.Now(), []Server{ssServer("p1", "")}, dns, false, false); err != nil {
		t.Fatalf("ProfileImport: %v", err)
	}
	ps, _ := store.LoadProfiles()
	if p, _ := ps.Get("provider"); p.DNS == nil {
		t.Fatal("ProfileImport did not store the captured policy")
	}

	// A refresh that declares no policy keeps the existing one.
	if _, _, err := store.ProfileImport("provider", "https://x", time.Now(), []Server{ssServer("p1", "")}, nil, true, false); err != nil {
		t.Fatalf("ProfileImport (refresh): %v", err)
	}
	ps, _ = store.LoadProfiles()
	if p, _ := ps.Get("provider"); p.DNS == nil {
		t.Error("a refresh without a policy erased the stored one")
	}

	// Clearing is explicit.
	if _, err := store.SetProfileDNS("provider", nil); err != nil {
		t.Fatalf("SetProfileDNS(clear): %v", err)
	}
	ps, _ = store.LoadProfiles()
	if p, _ := ps.Get("provider"); p.DNS != nil {
		t.Errorf("SetProfileDNS(nil) did not clear the policy: %+v", p.DNS)
	}
	if m := ps.DNSMap(); m != nil {
		t.Errorf("DNSMap is non-empty after clearing: %+v", m)
	}
}
