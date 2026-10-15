package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinRulesAreOrderedAndComplete(t *testing.T) {
	got := BuiltinRules()
	if len(got) == 0 {
		t.Fatal("BuiltinRules() is empty; the default policy must route something")
	}

	want := []struct {
		name     string
		outbound Outbound
	}{
		{"private-dest-direct", OutDirect},
		{"ads-block", OutBlock},
		{"service-overrides-proxy", OutProxy},
		{"cn-direct", OutDirect},
	}
	if len(got) != len(want) {
		t.Fatalf("BuiltinRules() has %d rules, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Name != w.name {
			t.Errorf("rule %d = %q, want %q (order is the policy)", i, got[i].Name, w.name)
		}
		if got[i].Outbound != w.outbound {
			t.Errorf("rule %q outbound = %q, want %q", got[i].Name, got[i].Outbound, w.outbound)
		}
		if err := got[i].Validate(); err != nil {
			t.Errorf("rule %q does not validate: %v", got[i].Name, err)
		}
	}

	// The service overrides are the only reason the China rule is not first,
	// so they must be evaluated before it.
	order := map[string]int{}
	for i, r := range got {
		order[r.Name] = i
	}
	if order["service-overrides-proxy"] > order["cn-direct"] {
		t.Error("service overrides are evaluated after cn-direct, so they could never win")
	}
	if order["ads-block"] > order["cn-direct"] {
		t.Error("ads-block is evaluated after cn-direct, so a CN ad domain would be dialled")
	}
	if order["private-dest-direct"] != 0 {
		t.Error("private destinations must be matched before anything that could proxy them")
	}
}

func TestBuiltinRulesCoverTheReferenceIntent(t *testing.T) {
	// The policy is derived from a hand-written config; these are the
	// categories that must be present for it to be a superset of that intent.
	refs := strings.Join(ReferencedRuleSets(), " ")
	for _, want := range []string{"geosite-cn", "geoip-cn", "geosite-category-ads-all", "geosite-steam"} {
		if !strings.Contains(refs, want) {
			t.Errorf("built-in policy does not reference %q (referenced: %s)", want, refs)
		}
	}
	// Every referenced set must be embedded, and ReferencedRuleSets must not
	// name the same set twice.
	seen := map[string]bool{}
	for _, tag := range ReferencedRuleSets() {
		if seen[tag] {
			t.Errorf("ReferencedRuleSets() lists %q twice", tag)
		}
		seen[tag] = true
		if _, ok := LookupRuleSet(tag); !ok {
			t.Errorf("referenced rule set %q is not embedded", tag)
		}
	}
}

func TestBuiltinActions(t *testing.T) {
	actions := BuiltinActions()
	if len(actions) != 2 {
		t.Fatalf("BuiltinActions() = %+v, want sniff + hijack-dns", actions)
	}
	if actions[0].Kind != ActionSniff {
		t.Errorf("first action = %q, want sniff: without sniffing no IP-addressed connection can match a domain rule", actions[0].Kind)
	}
	if actions[1].Kind != ActionHijackDNS || actions[1].Protocol != "dns" {
		t.Errorf("second action = %+v, want hijack-dns restricted to the sniffed dns protocol", actions[1])
	}
}

func TestBuiltinDNSIsSplit(t *testing.T) {
	dns := BuiltinDNS()
	if dns.Final != DNSRemote {
		t.Errorf("BuiltinDNS().Final = %q, want the proxied resolver %q", dns.Final, DNSRemote)
	}
	if dns.Strategy != "prefer_ipv4" {
		t.Errorf("BuiltinDNS().Strategy = %q, want prefer_ipv4", dns.Strategy)
	}
	if len(dns.Rules) != 1 || dns.Rules[0].Server != DNSCN {
		t.Fatalf("BuiltinDNS().Rules = %+v, want exactly the geosite-cn -> dns-cn split", dns.Rules)
	}
	if got := strings.Join(dns.Rules[0].RuleSets, ","); got != "geosite-cn" {
		t.Errorf("DNS rule matches %q, want geosite-cn", got)
	}

	byTag := map[string]DNSServer{}
	for _, s := range dns.Servers {
		byTag[s.Tag] = s
	}
	cn, ok := byTag[DNSCN]
	if !ok {
		t.Fatalf("BuiltinDNS() has no %s server", DNSCN)
	}
	if cn.Detour != "" {
		t.Errorf("%s must be reached directly, got detour %q", DNSCN, cn.Detour)
	}
	remote, ok := byTag[DNSRemote]
	if !ok {
		t.Fatalf("BuiltinDNS() has no %s server", DNSRemote)
	}
	if remote.Detour != string(OutProxy) {
		t.Errorf("%s detour = %q, want the proxy outbound: a foreign resolver reached directly is a leak", DNSRemote, remote.Detour)
	}

	// Global mode has no split to make, but it must still resolve through the
	// proxy.
	g := GlobalDNS()
	if len(g.Rules) != 0 {
		t.Errorf("GlobalDNS() carries rules %+v, want none", g.Rules)
	}
	if len(g.Servers) != 1 || g.Servers[0].Detour != string(OutProxy) {
		t.Errorf("GlobalDNS() = %+v, want a single proxied resolver", g.Servers)
	}
}

func TestRuleValidate(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantSub string
	}{
		{name: "suffix rule", rule: Rule{Name: "cn", Outbound: OutDirect, DomainSuffixes: []string{"cn"}}},
		{name: "block rule", rule: Rule{Name: "ads", Outbound: OutBlock, DomainKeywords: []string{"ads"}}},
		{name: "ip rule", rule: Rule{Name: "private", Outbound: OutDirect, IPCIDRs: []string{"10.0.0.0/8"}}},
		{name: "rule set", rule: Rule{Name: "cn-geo", Outbound: OutDirect, RuleSets: []string{"geosite-cn"}}},
		{name: "private flag alone", rule: Rule{Name: "lan", Outbound: OutDirect, IPIsPrivate: true}},
		{name: "port rule", rule: Rule{Name: "smtp", Outbound: OutBlock, Ports: []int{25}}},
		{name: "no name", rule: Rule{Outbound: OutDirect, Domains: []string{"example.com"}}, wantSub: "name must not be empty"},
		{name: "no outbound", rule: Rule{Name: "x", Domains: []string{"example.com"}}, wantSub: "unknown outbound"},
		{name: "no matcher", rule: Rule{Name: "x", Outbound: OutDirect}, wantSub: "matches nothing"},
		{name: "unknown rule set", rule: Rule{Name: "x", Outbound: OutDirect, RuleSets: []string{"geosite-nope"}}, wantSub: `unknown rule set "geosite-nope"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestSummaryMentionsTheMatchers(t *testing.T) {
	r := Rule{Name: "x", Outbound: OutDirect, RuleSets: []string{"geosite-cn", "geoip-cn"}, IPIsPrivate: true}
	got := r.Summary()
	for _, want := range []string{"rule_set=[geosite-cn geoip-cn]", "ip_is_private"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary() = %q, want it to contain %q", got, want)
		}
	}
	if a := (Action{Kind: ActionHijackDNS, Protocol: "dns"}).Summary(); a != "protocol=dns -> hijack-dns" {
		t.Errorf("action Summary() = %q", a)
	}
}

func TestEmbeddedRuleSetsMatchPinnedChecksums(t *testing.T) {
	for _, rs := range RuleSets() {
		data, err := RuleSetBytes(rs.Tag)
		if err != nil {
			t.Fatalf("RuleSetBytes(%q): %v", rs.Tag, err)
		}
		if len(data) == 0 {
			t.Errorf("rule set %q is empty", rs.Tag)
		}
		if got := sha256Hex(data); got != rs.SHA256 {
			t.Errorf("rule set %q: embedded sha256 = %s, want %s (re-run scripts/fetch-rules.sh)", rs.Tag, got, rs.SHA256)
		}
		// The file name is the flattened upstream path: geo/geosite/cn.srs ->
		// geosite-cn.srs, which is also the rule_set tag sing-box sees.
		flattened := strings.ReplaceAll(strings.TrimPrefix(rs.Source, "geo/"), "/", "-")
		if rs.File != flattened {
			t.Errorf("rule set %q: File %q does not match the flattened source %q", rs.Tag, rs.File, flattened)
		}
		if rs.File != rs.Tag+".srs" {
			t.Errorf("rule set %q: File %q is not <tag>.srs", rs.Tag, rs.File)
		}
		if filepath.Ext(rs.File) != ".srs" {
			t.Errorf("rule set %q: File %q is not a .srs", rs.Tag, rs.File)
		}
		if rs.Description == "" {
			t.Errorf("rule set %q has no description", rs.Tag)
		}
		if kind := rs.Kind(); kind != "geosite" && kind != "geoip" {
			t.Errorf("rule set %q: Kind() = %q", rs.Tag, kind)
		}
	}
}

// The fetch script pins the same table; if the two drift, a re-fetch either
// misses a set or installs one vender does not know about.
func TestFetchScriptPinMatchesEmbeddedTable(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "fetch-rules.sh"))
	if err != nil {
		t.Fatalf("read scripts/fetch-rules.sh: %v", err)
	}
	body := string(script)

	commitLine := `RULES_SOURCE_COMMIT="` + RuleSetSourceCommit + `"`
	if !strings.Contains(body, commitLine) {
		t.Errorf("scripts/fetch-rules.sh does not pin RuleSetSourceCommit (%s)", RuleSetSourceCommit)
	}
	for _, rs := range RuleSets() {
		line := rs.Tag + "|" + rs.Source + "|" + rs.SHA256
		if !strings.Contains(body, line) {
			t.Errorf("scripts/fetch-rules.sh has no table row %q", line)
		}
	}
}

func TestLookupAndBytesRejectUnknownTags(t *testing.T) {
	if _, ok := LookupRuleSet("geosite-nope"); ok {
		t.Error("LookupRuleSet accepted an unknown tag")
	}
	if _, err := RuleSetBytes("geosite-nope"); err == nil {
		t.Error("RuleSetBytes accepted an unknown tag")
	}
	if _, err := RuleSetPath(t.TempDir(), "geosite-nope"); err == nil {
		t.Error("RuleSetPath accepted an unknown tag")
	}
}

func TestRuleSetPathIsAbsolute(t *testing.T) {
	got, err := RuleSetPath("relative/run", "geosite-cn")
	if err != nil {
		t.Fatalf("RuleSetPath: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("RuleSetPath(%q) = %q, want an absolute path so sing-box check works from any cwd", "relative/run", got)
	}
	if filepath.Base(got) != "geosite-cn.srs" {
		t.Errorf("RuleSetPath = %q, want it to end in geosite-cn.srs", got)
	}
	if !strings.Contains(got, RulesSubdir) {
		t.Errorf("RuleSetPath = %q, want it inside the %q subdirectory", got, RulesSubdir)
	}
}

func TestWriteRuleSetsIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	n, err := WriteRuleSets(dir)
	if err != nil {
		t.Fatalf("WriteRuleSets: %v", err)
	}
	if n != len(RuleSets()) {
		t.Errorf("WriteRuleSets wrote %d files, want %d", n, len(RuleSets()))
	}
	for _, rs := range RuleSets() {
		path := filepath.Join(dir, rs.File)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want, _ := RuleSetBytes(rs.Tag)
		if string(got) != string(want) {
			t.Errorf("%s does not match the embedded bytes", path)
		}
	}
	// A second pass must notice the files are current and rewrite nothing.
	again, err := WriteRuleSets(dir)
	if err != nil {
		t.Fatalf("second WriteRuleSets: %v", err)
	}
	if again != 0 {
		t.Errorf("WriteRuleSets rewrote %d unchanged files", again)
	}
	// A truncated file must be repaired.
	rs := RuleSets()[0]
	if err := os.WriteFile(filepath.Join(dir, rs.File), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixed, err := WriteRuleSets(dir)
	if err != nil {
		t.Fatalf("repair WriteRuleSets: %v", err)
	}
	if fixed != 1 {
		t.Errorf("WriteRuleSets repaired %d files, want 1", fixed)
	}
}

func TestEmbeddedFileListMatchesTable(t *testing.T) {
	names, err := EmbeddedFileList()
	if err != nil {
		t.Fatalf("EmbeddedFileList: %v", err)
	}
	if len(names) != len(RuleSets()) {
		t.Fatalf("embedded assets = %d files, table = %d entries: %v", len(names), len(RuleSets()), names)
	}
	for _, rs := range RuleSets() {
		found := false
		for _, n := range names {
			if n == rs.File {
				found = true
			}
		}
		if !found {
			t.Errorf("table entry %q has no embedded file %q", rs.Tag, rs.File)
		}
	}
}

func TestRuleSetSize(t *testing.T) {
	size, err := RuleSetSize("geoip-cn")
	if err != nil {
		t.Fatalf("RuleSetSize: %v", err)
	}
	if size <= 0 {
		t.Errorf("RuleSetSize(geoip-cn) = %d", size)
	}
}
