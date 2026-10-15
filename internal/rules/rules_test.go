package rules

import (
	"strings"
	"testing"
)

func TestBuiltinRulesIsEmptyUntilM2(t *testing.T) {
	if got := BuiltinRules(); got != nil {
		t.Errorf("BuiltinRules() = %#v, want nil until M2", got)
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
		{name: "port rule", rule: Rule{Name: "smtp", Outbound: OutBlock, Ports: []int{25}}},
		{name: "no name", rule: Rule{Outbound: OutDirect, Domains: []string{"example.com"}}, wantSub: "name must not be empty"},
		{name: "no outbound", rule: Rule{Name: "x", Domains: []string{"example.com"}}, wantSub: "unknown outbound"},
		{name: "no matcher", rule: Rule{Name: "x", Outbound: OutDirect}, wantSub: "matches nothing"},
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
