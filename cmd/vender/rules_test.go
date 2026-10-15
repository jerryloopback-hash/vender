package main

import (
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/rules"
)

func TestCLIRulesListShowsThePolicy(t *testing.T) {
	out, err := runCLI(t, "rules", "list")
	if err != nil {
		t.Fatalf("rules list: %v", err)
	}
	// Every rule, in order, with its named outbound.
	last := -1
	for _, r := range rules.BuiltinRules() {
		if !strings.Contains(out, r.Name) {
			t.Errorf("rules list does not mention rule %q", r.Name)
		}
		i := strings.Index(out, r.Name)
		if i < last {
			t.Errorf("rules list prints %q out of order (the order is the policy)", r.Name)
		}
		last = i
	}
	// The actions and the DNS policy are part of the policy too.
	for _, want := range []string{"sniff", "hijack-dns", "dns-cn", "dns-remote", "prefer_ipv4", "final"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules list output does not mention %q", want)
		}
	}
	// Every embedded rule set, with its provenance and size.
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

func TestCLIRulesListRejectsArgs(t *testing.T) {
	if _, err := runCLI(t, "rules", "list", "extra"); err == nil {
		t.Error("rules list extra = nil error, want a usage error")
	}
}
