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
	if dns.Final != DNSCN {
		t.Errorf("BuiltinDNS().Final = %q, want the direct domestic resolver %q", dns.Final, DNSCN)
	}
	if dns.Strategy != "prefer_ipv4" {
		t.Errorf("BuiltinDNS().Strategy = %q, want prefer_ipv4", dns.Strategy)
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
	if cn.Server == "" || cn.Type != "udp" {
		t.Errorf("%s must be a domestic UDP resolver, got %+v", DNSCN, cn)
	}
	fake, ok := byTag[DNSFakeIP]
	if !ok {
		t.Fatalf("BuiltinDNS() has no %s server", DNSFakeIP)
	}
	if fake.Type != "fakeip" || fake.Server != "" {
		t.Errorf("%s must be a local FakeIP server with no upstream, got %+v", DNSFakeIP, fake)
	}
	if fake.Inet4Range == "" {
		t.Errorf("%s has no inet4_range", DNSFakeIP)
	}

	// No DNS server may be reached through the proxy in rule mode: that is the
	// exact design failure M5 fixes.
	for _, s := range dns.Servers {
		if s.Detour == string(OutProxy) {
			t.Errorf("rule-mode DNS server %q is reached through the proxy", s.Tag)
		}
	}

	// The rules mirror the routing split: real resolution for what goes direct,
	// FakeIP for what is proxied.
	var cnRule, fakeRule *DNSRule
	for i := range dns.Rules {
		r := &dns.Rules[i]
		switch r.Name {
		case "critical-direct-real":
			if len(r.DomainSuffixes) == 0 {
				t.Error("the critical rule matches no domain")
			}
			if r.Server != DNSCN {
				t.Errorf("the critical rule resolves via %q, want the direct resolver", r.Server)
			}
		case "cn-domains-real":
			cnRule = r
		case "proxied-fakeip":
			fakeRule = r
		}
	}
	if cnRule == nil || cnRule.Server != DNSCN {
		t.Fatalf("no geosite-cn -> %s DNS rule: %+v", DNSCN, dns.Rules)
	}
	if got := strings.Join(cnRule.RuleSets, ","); got != "geosite-cn" {
		t.Errorf("the CN rule matches %q, want geosite-cn", got)
	}
	if fakeRule == nil || fakeRule.Server != DNSFakeIP {
		t.Fatalf("no address-query -> %s DNS rule: %+v", DNSFakeIP, dns.Rules)
	}
	if got := strings.Join(fakeRule.QueryTypes, ","); got != "A,AAAA" {
		t.Errorf("the FakeIP rule restricts query types to %q, want A,AAAA", got)
	}
	// Rule order is the policy: the FakeIP catch-all must be last so the
	// specific (direct) rules win.
	if dns.Rules[len(dns.Rules)-1].Name != "proxied-fakeip" {
		t.Errorf("the FakeIP rule is not last: %+v", dns.Rules)
	}

	// Global mode has no split to make, but it must still resolve through the
	// proxy, because in global mode everything is proxied anyway.
	g := GlobalDNS()
	if len(g.Rules) != 0 {
		t.Errorf("GlobalDNS() carries rules %+v, want none", g.Rules)
	}
	if len(g.Servers) != 1 || g.Servers[0].Detour != string(OutProxy) {
		t.Errorf("GlobalDNS() = %+v, want a single proxied resolver", g.Servers)
	}
}

func TestCriticalListIsTinyAndDirect(t *testing.T) {
	suffixes := CriticalDirectSuffixes()
	if len(suffixes) == 0 || len(suffixes) > 5 {
		t.Fatalf("CriticalDirectSuffixes() = %v, want a tiny list", suffixes)
	}
	for _, want := range []string{"opencode.ai"} {
		found := false
		for _, s := range suffixes {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the critical list does not contain %q", want)
		}
	}
	// api.tavily.com was removed: it is not reliably reachable directly on this
	// network, so forcing it direct broke it. It must follow the normal policy
	// and stay out of the unoverrideable list.
	for _, banned := range []string{"api.tavily.com"} {
		for _, s := range suffixes {
			if s == banned {
				t.Errorf("the critical list still contains %q", banned)
			}
		}
	}
	rules := CriticalRules()
	if len(rules) != 1 || rules[0].Outbound != OutDirect {
		t.Fatalf("CriticalRules() = %+v, want one direct rule", rules)
	}
	if err := rules[0].Validate(); err != nil {
		t.Errorf("the critical rule does not validate: %v", err)
	}
}

func TestEffectiveRulesOrder(t *testing.T) {
	user := &UserRules{
		Direct: []string{"always.direct.example"},
		Proxy:  []string{"force.proxy.example"},
		Block:  []string{"block.this.example"},
	}
	policy, err := EffectiveRules(user)
	if err != nil {
		t.Fatalf("EffectiveRules: %v", err)
	}
	if policy[0].Name != "critical-direct" {
		t.Errorf("policy[0] = %q, want the critical list first", policy[0].Name)
	}
	// The user rules come next, in direct/proxy/block order, then the built-in
	// policy.
	want := []string{"critical-direct", "user-direct", "user-proxy", "user-block"}
	for i, name := range want {
		if i >= len(policy) {
			t.Fatalf("policy has %d rules, want at least %d", len(policy), len(want))
		}
		if policy[i].Name != name {
			t.Errorf("policy[%d] = %q, want %q", i, policy[i].Name, name)
		}
	}
	builtin := len(policy) - len(BuiltinRules())
	if builtin < len(want) {
		t.Fatalf("the built-in policy is not at the end: %+v", policy)
	}
	for i, r := range BuiltinRules() {
		if policy[builtin+i].Name != r.Name {
			t.Errorf("built-in rule %q is not at index %d", r.Name, builtin+i)
		}
	}
}

func TestUserRulesParseAndCompile(t *testing.T) {
	data := []byte(`
direct:
  - opencode.ai
  - =exact.example
  - .tld
  - 10.0.0.0/8
proxy:
  - force.proxy
block:
  - cidr:192.0.2.0/24
`)
	u, err := ParseUserRules(data)
	if err != nil {
		t.Fatalf("ParseUserRules: %v", err)
	}
	if err := u.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, err := u.Rules()
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Rules() = %+v, want direct, proxy and block", got)
	}
	if got[0].Name != "user-direct" || got[0].Outbound != OutDirect {
		t.Errorf("direct rule = %+v", got[0])
	}
	// Bare and .prefixed entries are suffixes; = is an exact domain; a slash
	// is a CIDR.
	if strings.Join(got[0].DomainSuffixes, ",") != "opencode.ai,tld" {
		t.Errorf("direct suffixes = %v", got[0].DomainSuffixes)
	}
	if strings.Join(got[0].Domains, ",") != "exact.example" {
		t.Errorf("direct domains = %v", got[0].Domains)
	}
	if strings.Join(got[0].IPCIDRs, ",") != "10.0.0.0/8" {
		t.Errorf("direct cidrs = %v", got[0].IPCIDRs)
	}
	if strings.Join(got[2].IPCIDRs, ",") != "192.0.2.0/24" {
		t.Errorf("block cidrs = %v", got[2].IPCIDRs)
	}

	if err := (&UserRules{Direct: []string{"has space.example"}}).Validate(); err == nil {
		t.Error("a domain with a space validated")
	}
	if err := (&UserRules{Proxy: []string{"10.0.0.0/99"}}).Validate(); err == nil {
		t.Error("a bad CIDR validated")
	}
}

func TestRuleModeDNSMirrorsUserDirectList(t *testing.T) {
	user := &UserRules{Direct: []string{"internal.corp"}}
	dns := RuleModeDNS(user)
	var found bool
	for _, r := range dns.Rules {
		if r.Name == "user-direct-real" {
			found = true
			if r.Server != DNSCN {
				t.Errorf("user direct rule resolves via %q, want %s", r.Server, DNSCN)
			}
			if strings.Join(r.DomainSuffixes, ",") != "internal.corp" {
				t.Errorf("user direct suffixes = %v", r.DomainSuffixes)
			}
		}
	}
	if !found {
		t.Errorf("RuleModeDNS did not add a user direct rule: %+v", dns.Rules)
	}
	// Without user rules the policy is exactly the built-in one.
	if len(BuiltinDNS().Rules) != len(RuleModeDNS(nil).Rules) {
		t.Error("BuiltinDNS is not RuleModeDNS(nil)")
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
