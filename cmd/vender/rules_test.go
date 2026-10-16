package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/rules"
)

func TestCLIRulesListShowsThePolicy(t *testing.T) {
	cfgDir := t.TempDir()
	// A user file with one entry in each list, so the test also proves the
	// merge prints the user rules, the critical list and the built-in policy in
	// evaluation order.
	user := "direct:\n  - opencode.ai\nproxy:\n  - example.net\nblock:\n  - ads.example\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "rules.yaml"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLIIn(t, t.TempDir(), "rules", "list", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("rules list: %v", err)
	}

	// The critical list comes first, then the user rules, then the built-in
	// policy, all in one order.
	order := []string{"critical-direct", "user-direct", "user-proxy", "user-block", "private-dest-direct", "ads-block", "cn-direct"}
	last := -1
	for _, name := range order {
		i := strings.Index(out, name)
		if i < 0 {
			t.Errorf("rules list does not mention %q", name)
			continue
		}
		if i < last {
			t.Errorf("rules list prints %q out of order", name)
		}
		last = i
	}

	// The actions, the new split DNS and its resolvers are part of the policy.
	for _, want := range []string{"sniff", "hijack-dns", "dns-cn", "dns-fakeip", "prefer_ipv4", "final"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules list output does not mention %q", want)
		}
	}
	// The old proxied resolver must be gone from rule mode: routing a query
	// through the proxy is exactly the failure M5 fixes.
	if strings.Contains(out, "dns-remote") {
		t.Errorf("rules list still mentions the proxied resolver dns-remote:\n%s", out)
	}

	// Every embedded rule set, with its provenance.
	for _, rs := range rules.RuleSets() {
		if !strings.Contains(out, rs.Tag) {
			t.Errorf("rules list does not mention the embedded rule set %q", rs.Tag)
		}
		if !strings.Contains(out, rs.Source) {
			t.Errorf("rules list does not show the source of %q", rs.Tag)
		}
	}
	if !strings.Contains(out, rules.RuleSetSourceRepo) {
		t.Errorf("rules list does not name the upstream repository")
	}
}

func TestCLIRulesListWithoutAUserFile(t *testing.T) {
	out, err := runCLIIn(t, t.TempDir(), "rules", "list", "--config-dir", t.TempDir())
	if err != nil {
		t.Fatalf("rules list: %v", err)
	}
	if !strings.Contains(out, "user rules: none") {
		t.Errorf("rules list does not say the user file is absent:\n%s", out)
	}
	if !strings.Contains(out, "critical-direct") {
		t.Errorf("the critical list must be shown even with no user file")
	}
}

func TestCLIRulesInit(t *testing.T) {
	cfgDir := t.TempDir()
	out, err := runCLIIn(t, t.TempDir(), "rules", "init", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("rules init: %v", err)
	}
	if !strings.Contains(out, "Wrote") {
		t.Errorf("rules init output = %q, want a write confirmation", out)
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, "rules.yaml"))
	if err != nil {
		t.Fatalf("rules.yaml was not created: %v", err)
	}
	if _, err := rules.ParseUserRules(data); err != nil {
		t.Errorf("the template does not parse: %v", err)
	}

	// A second run must not clobber it.
	if err := os.WriteFile(filepath.Join(cfgDir, "rules.yaml"), []byte("direct:\n  - keep.me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runCLIIn(t, t.TempDir(), "rules", "init", "--config-dir", cfgDir)
	if err != nil {
		t.Fatalf("second rules init: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("second rules init output = %q, want it to refuse to overwrite", out)
	}
	data, _ = os.ReadFile(filepath.Join(cfgDir, "rules.yaml"))
	if !strings.Contains(string(data), "keep.me") {
		t.Errorf("rules init clobbered the existing file: %s", data)
	}
}

func TestCLIRulesListRejectsArgs(t *testing.T) {
	if _, err := runCLI(t, "rules", "list", "extra"); err == nil {
		t.Error("rules list extra = nil error, want a usage error")
	}
}
