package rules

import (
	"reflect"
	"strings"
	"testing"
)

func domainMatch(host string, domains, suffixes []string) bool {
	for _, d := range domains {
		if host == d {
			return true
		}
	}
	for _, s := range suffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

func TestBingSearchExceptionIsNarrow(t *testing.T) {
	var search Rule
	for _, r := range BuiltinRules() {
		if r.Name == "bing-search-direct" {
			search = r
		}
	}
	if search.Outbound != OutDirect {
		t.Fatal("missing Bing direct exception")
	}
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"bing.com", true}, {"www.bing.com", true}, {"cn.bing.com", true},
		{"global.bing.com", true}, {"www.bing.com.cn", true},
		{"r.bing.com", true}, {"th.bing.com", true},
		{"s.cn.bing.net", true}, {"s.bing.net", true},
		{"tse1.mm.bing.net", true}, {"ts1.cn.mm.bing.net", true},
		{"edgeservices.bing.com", false}, {"sydney.bing.com", false},
		{"copilot.microsoft.com", false}, {"api.bing.com", false},
		{"www.bing.com.evil.example", false}, {"notbing.com", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			if got := domainMatch(tc.host, search.Domains, search.DomainSuffixes); got != tc.want {
				t.Errorf("search exception = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDNSProjectsEffectiveDomainPolicyInOrder(t *testing.T) {
	user := &UserRules{
		Direct: UserList{"=exact.example", "overlap.example", "10.0.0.0/8"},
		Proxy:  UserList{"overlap.example", "cn.bing.com"},
		Block:  UserList{"blocked.example"},
	}
	policy, err := EffectiveRules(user)
	if err != nil {
		t.Fatal(err)
	}
	dns := RuleModeDNS(user)
	i := 0
	for _, r := range policy {
		var sets []string
		for _, tag := range r.RuleSets {
			if set, ok := LookupRuleSet(tag); ok && set.Kind() == "geosite" {
				sets = append(sets, tag)
			}
		}
		if len(sets)+len(r.Domains)+len(r.DomainSuffixes) == 0 {
			continue
		}
		d := dns.Rules[i]
		i++
		if !reflect.DeepEqual(d.RuleSets, sets) || !reflect.DeepEqual(d.Domains, r.Domains) ||
			!reflect.DeepEqual(d.DomainSuffixes, r.DomainSuffixes) {
			t.Fatalf("DNS rule %d does not mirror route %q: %+v", i, r.Name, d)
		}
		switch r.Outbound {
		case OutDirect:
			if d.Server != DNSCN || len(d.QueryTypes) != 0 {
				t.Errorf("direct rule %q: %+v", r.Name, d)
			}
		case OutProxy:
			if d.Server != DNSFakeIP || strings.Join(d.QueryTypes, ",") != "A,AAAA" {
				t.Errorf("proxy rule %q: %+v", r.Name, d)
			}
		case OutBlock:
			if d.Action != "reject" || d.Server != "" {
				t.Errorf("block rule %q: %+v", r.Name, d)
			}
		}
	}
	if len(dns.Rules) != i+1 || dns.Rules[i].Name != "proxied-fakeip" {
		t.Fatalf("expected only the address-query fallback after projected rules: %+v", dns.Rules)
	}
}

func TestDNSOverlapPrecedence(t *testing.T) {
	// Model the relevant overlapping upstream categories. Engine-backed tests
	// also validate the generated JSON against the real sing-box binary.
	sets := map[string][]string{
		"geosite-cn":               {"cn.bing.com", "download.microsoft.com", "force-proxy.example", "ad.example"},
		"geosite-microsoft":        {"bing.com", "microsoft.com"},
		"geosite-category-ads-all": {"ad.example"},
	}
	dns := RuleModeDNS(&UserRules{
		Direct: UserList{"direct.example", "both.example"},
		Proxy:  UserList{"force-proxy.example", "both.example"},
		Block:  UserList{"block.example"},
	})
	for _, tc := range []struct{ host, want string }{
		{"www.bing.com", DNSCN}, {"cn.bing.com", DNSCN},
		{"download.microsoft.com", DNSFakeIP},
		{"edgeservices.bing.com", DNSFakeIP}, {"sydney.bing.com", DNSFakeIP},
		{"direct.example", DNSCN}, {"both.example", DNSCN},
		{"force-proxy.example", DNSFakeIP}, {"block.example", "reject"},
		{"ad.example", "reject"}, {"unknown.example", DNSFakeIP},
	} {
		t.Run(tc.host, func(t *testing.T) {
			got := dns.Final
			for _, r := range dns.Rules {
				match := domainMatch(tc.host, r.Domains, r.DomainSuffixes)
				for _, tag := range r.RuleSets {
					match = match || domainMatch(tc.host, nil, sets[tag])
				}
				if !match && len(r.QueryTypes) > 0 && len(r.RuleSets)+len(r.Domains)+len(r.DomainSuffixes) == 0 {
					match = true // address-query catch-all
				}
				if match {
					got = r.Server
					if r.Action == "reject" {
						got = "reject"
					}
					break
				}
			}
			if got != tc.want {
				t.Errorf("DNS destination = %q, want %q", got, tc.want)
			}
		})
	}
}
