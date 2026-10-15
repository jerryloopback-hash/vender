package rules

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RulesSubdir is where the embedded rule sets are materialised under the
// engine's run directory before sing-box starts. sing-box's route.rule_set
// only understands local files, so "全部内置" means: ship the bytes inside the
// binary, write them out at start-up, point sing-box at the copies.
const RulesSubdir = "rules"

// Provenance of the embedded rule sets. scripts/fetch-rules.sh pins the same
// values; a unit test checks that the two tables agree and that every embedded
// file still hashes to its recorded SHA-256.
const (
	// RuleSetSourceRepo is the upstream repository.
	RuleSetSourceRepo = "MetaCubeX/meta-rules-dat"
	// RuleSetSourceBranch is the branch the commit was taken from.
	RuleSetSourceBranch = "sing"
	// RuleSetSourceCommit is the pinned commit. A commit, not a branch or the
	// moving `latest` tag, so a rebuild fetches byte-identical inputs.
	RuleSetSourceCommit = "d1c433471f5b6b77051616cfa4a5279d5092f514"
	// RuleSetSourceDate is the committer date of the pinned commit.
	RuleSetSourceDate = "2026-10-15"
)

//go:embed assets/*.srs
var assetsFS embed.FS

// RuleSet is one compiled rule set shipped inside the binary.
//
// The files are the sing-box binary rule-set format ("SRS"), the same artifact
// a `route.rule_set` of type "local" consumes. They are the compiled form of
// the geo data the reference config spelled out as thousands of individual
// domain rules.
type RuleSet struct {
	// Tag is vender's name for the set. It is used verbatim as the sing-box
	// rule_set tag, so rules and DNS rules can refer to it by name.
	Tag string
	// File is the file name inside assets/ (and inside the extracted dir).
	File string
	// Source is the upstream path, for documentation and review.
	Source string
	// SHA256 is the upstream file's checksum, re-verified against the embedded
	// bytes by TestEmbeddedRuleSetsMatchPinnedChecksums.
	SHA256 string
	// Description says what the set is for, shown by `vender rules list`.
	Description string
}

// Kind reports the upstream family: "geosite" (domains) or "geoip" (IP ranges).
func (r RuleSet) Kind() string {
	if tag, ok := strings.CutPrefix(r.Tag, "geo"); ok {
		if k, _, found := strings.Cut(tag, "-"); found {
			return "geo" + k
		}
	}
	return "unknown"
}

// ruleSets is the pinned table, kept in sync with scripts/fetch-rules.sh.
var ruleSets = []RuleSet{
	{Tag: "geosite-cn", File: "geosite-cn.srs", Source: "geo/geosite/cn.srs",
		SHA256:      "2ee13c9d9bc30b35554767fecc2f9511413b00e362b02fa6466800cce772fe20",
		Description: "Chinese sites and services, the '国内网站' list"},
	{Tag: "geosite-category-ads-all", File: "geosite-category-ads-all.srs", Source: "geo/geosite/category-ads-all.srs",
		SHA256:      "5d7e3f38ceb008c793f12c4b84108ed61c64ae11e16a920af961e0c33a7e9233",
		Description: "Aggregated advertising, tracking and telemetry domains"},
	{Tag: "geoip-cn", File: "geoip-cn.srs", Source: "geo/geoip/cn.srs",
		SHA256:      "4cdb3d4e6b82b5fb0603cfc078b80f775344a0f71f586dbce4d0ee47ac75a070",
		Description: "IPv4/IPv6 ranges allocated in mainland China"},
	{Tag: "geosite-steam", File: "geosite-steam.srs", Source: "geo/geosite/steam.srs",
		SHA256:      "f9d7c11577f86eeaa315b188742cc40db1ef45994e29cb773c6268166b04c7bc",
		Description: "Steam store, community and downloads"},
	{Tag: "geosite-onedrive", File: "geosite-onedrive.srs", Source: "geo/geosite/onedrive.srs",
		SHA256:      "ecf26c6c2f999cc409fd2053128e361b48c1d7cdce130056f3cd2a6a74b0251c",
		Description: "OneDrive"},
	{Tag: "geosite-microsoft", File: "geosite-microsoft.srs", Source: "geo/geosite/microsoft.srs",
		SHA256:      "3f84b0370ecb594755d84f9e1ed6cbf0eca1814431b7f8598f0b49c2ea52e25f",
		Description: "Microsoft services (the upstream list also covers GitHub)"},
	{Tag: "geosite-google", File: "geosite-google.srs", Source: "geo/geosite/google.srs",
		SHA256:      "1f1a046775fbf136da45ab4435cee9450e0b98cab1f429e79d54604f073f8b15",
		Description: "Google services"},
	{Tag: "geosite-youtube", File: "geosite-youtube.srs", Source: "geo/geosite/youtube.srs",
		SHA256:      "ae9a576c046627c01bc1488f526775dfe919f1152c3ce6f75b678fce92498b3f",
		Description: "YouTube"},
	{Tag: "geosite-telegram", File: "geosite-telegram.srs", Source: "geo/geosite/telegram.srs",
		SHA256:      "30cd392b1d79b0569af077874b36c6892bf13a94dce0d8e4410c3b1976d62ea2",
		Description: "Telegram"},
	{Tag: "geosite-netflix", File: "geosite-netflix.srs", Source: "geo/geosite/netflix.srs",
		SHA256:      "40c5f50c80ceaba2a5e0b5c5760c9c5fce0b08498501397d8264dcae2aeeda53",
		Description: "Netflix"},
	{Tag: "geosite-openai", File: "geosite-openai.srs", Source: "geo/geosite/openai.srs",
		SHA256:      "d2c8fffc93d82084c4a01955891195cb7df84e1a804200de9e41868ac99e723b",
		Description: "OpenAI / ChatGPT"},
	{Tag: "geosite-bahamut", File: "geosite-bahamut.srs", Source: "geo/geosite/bahamut.srs",
		SHA256:      "b9ef2e31a6b359736aa618f2a717aa3530b66394a6c658dd0f16b77b67ef6174",
		Description: "Bahamut / 動畫瘋 (gamer.com.tw)"},
}

// RuleSets returns every embedded rule set in table order. The result is a
// copy; callers must not mutate the table.
func RuleSets() []RuleSet {
	out := make([]RuleSet, len(ruleSets))
	copy(out, ruleSets)
	return out
}

// LookupRuleSet returns the rule set with the given tag.
func LookupRuleSet(tag string) (RuleSet, bool) {
	for _, rs := range ruleSets {
		if rs.Tag == tag {
			return rs, true
		}
	}
	return RuleSet{}, false
}

// RuleSetBytes returns the embedded contents of a rule set.
func RuleSetBytes(tag string) ([]byte, error) {
	rs, ok := LookupRuleSet(tag)
	if !ok {
		return nil, fmt.Errorf("rules: unknown rule set %q", tag)
	}
	data, err := assetsFS.ReadFile("assets/" + rs.File)
	if err != nil {
		return nil, fmt.Errorf("rules: read embedded %s: %w", rs.File, err)
	}
	return data, nil
}

// RuleSetSize returns the size in bytes of an embedded rule set.
func RuleSetSize(tag string) (int, error) {
	data, err := RuleSetBytes(tag)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

// RuleSetPath returns the absolute path sing-box will read a rule set from for
// a run rooted at runDir.
//
// The path is absolute on purpose: sing-box resolves a relative rule_set path
// against its working directory, which would make `sing-box check -c
// <runDir>/singbox.json` fail from anywhere else.
func RuleSetPath(runDir, tag string) (string, error) {
	rs, ok := LookupRuleSet(tag)
	if !ok {
		return "", fmt.Errorf("rules: unknown rule set %q", tag)
	}
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return "", fmt.Errorf("rules: resolve run dir %q: %w", runDir, err)
	}
	return filepath.Join(abs, RulesSubdir, rs.File), nil
}

// WriteRuleSets materialises every embedded rule set into dir, creating it if
// needed. Files whose content already matches are left untouched, so starting
// the daemon repeatedly does not rewrite half a megabyte each time. It returns
// how many files were (re)written.
func WriteRuleSets(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("rules: create %s: %w", dir, err)
	}
	written := 0
	for _, rs := range ruleSets {
		data, err := assetsFS.ReadFile("assets/" + rs.File)
		if err != nil {
			return written, fmt.Errorf("rules: read embedded %s: %w", rs.File, err)
		}
		path := filepath.Join(dir, rs.File)
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := writeFileAtomic(path, data, 0o644); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// EmbeddedFileList returns the embedded file names, for tests and diagnostics.
func EmbeddedFileList() ([]string, error) {
	entries, err := fs.ReadDir(assetsFS, "assets")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// sha256Hex is used by the tests to re-verify the embedded assets.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so sing-box never reads a half-written rule set.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("rules: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("rules: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("rules: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("rules: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rules: replace %s: %w", path, err)
	}
	return nil
}
