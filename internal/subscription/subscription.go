// Package subscription fetches and parses proxy subscriptions: the config a
// commercial provider ("机场") hands out as a URL.
//
// Three shapes are supported, and they are auto-detected:
//
//  1. base64 of newline-separated share links (the classic v2ray subscription),
//  2. plain newline-separated share links,
//  3. Clash / Mihomo YAML with a `proxies:` list.
//
// Parsing is pure (Parse, ParseLinks, ParseClash) so it can be tested against
// fixtures with no network; Fetch is the only function that touches it. The
// package imports internal/config for the share-link parser and never imports
// the CLI.
package subscription

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ll-lite/vender/internal/config"
)

// UserAgent is the agent vender sends when fetching a subscription. Panels
// branch on it: a recognised v2ray client is served share links, while an
// unknown agent often gets an HTML page (or a Clash config) instead.
const UserAgent = "v2rayN/6.0"

// DefaultTimeout bounds one subscription fetch.
const DefaultTimeout = 30 * time.Second

// MaxBody bounds how much of a subscription vender will read, so a hostile or
// broken endpoint cannot exhaust memory.
const MaxBody = 8 << 20 // 8 MiB

// Format identifies the accepted subscription shapes.
type Format string

const (
	// FormatLinks is newline-separated share links.
	FormatLinks Format = "links"
	// FormatBase64Links is a base64 blob of newline-separated share links.
	FormatBase64Links Format = "base64-links"
	// FormatClash is Clash / Mihomo YAML with a proxies list.
	FormatClash Format = "clash"
)

// Skipped records one node or line the parser could not turn into a server.
type Skipped struct {
	// Node is the server name when one was known, else a clipped copy of the
	// offending line. It never contains a full credential-bearing link.
	Node string
	// Reason is the actionable explanation ("ss: this server needs a SIP003
	// plugin...").
	Reason string
}

// Result is the outcome of parsing one subscription body.
type Result struct {
	Servers []config.Server
	Skipped []Skipped
	// DNS is the DNS policy the subscription declared for its node hostnames
	// (see config.ProfileDNS). It is nil for a link list, which carries no
	// such policy, and for a Clash document that does not declare one.
	DNS *config.ProfileDNS
}

// ErrEmpty marks a subscription that parsed to nothing usable.
var ErrEmpty = errors.New("subscription contains no usable server")

// Parse auto-detects the subscription shape and parses it.
func Parse(data []byte) (Result, Format, error) {
	text := string(data)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Result{}, "", fmt.Errorf("subscription is empty")
	}

	if isClash(trimmed) {
		res, err := ParseClash(data)
		return res, FormatClash, err
	}

	// Plain links first: it is the cheapest check and the most common shape
	// when a panel ignores the user agent.
	if res := ParseLinks(text); len(res.Servers) > 0 {
		return res, FormatLinks, nil
	}

	// Then a base64 blob of the same.
	if decoded, err := decodeBase64(trimmed); err == nil {
		if res := ParseLinks(decoded); len(res.Servers) > 0 {
			return res, FormatBase64Links, nil
		}
	}

	// Finally, YAML that did not look like Clash at a glance.
	if res, err := ParseClash(data); err == nil && len(res.Servers) > 0 {
		return res, FormatClash, nil
	}

	return Result{}, "", fmt.Errorf("%w: not base64 share links, plain share links, or Clash YAML", ErrEmpty)
}

// isClash reports whether the body looks like a Clash / Mihomo document. The
// check is deliberately cheap: a `proxies:` (or legacy `Proxy:`) key, or any
// `proxy-providers:` block.
func isClash(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case l == "proxies:" || l == "Proxy:":
			return true
		case strings.HasPrefix(l, "proxy-providers:"):
			return true
		}
	}
	return false
}

// ParseLinks parses newline-separated share links. Blank lines and `#` / `//`
// comments are ignored. A line that is not a usable link is recorded in
// Skipped rather than failing the whole subscription.
func ParseLinks(text string) Result {
	var res Result
	seen := make(map[string]bool)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		srv, err := config.ParseShareLink(line)
		if err != nil {
			res.Skipped = append(res.Skipped, Skipped{Node: clipLink(line), Reason: err.Error()})
			continue
		}
		if verr := srv.Validate(); verr != nil {
			res.Skipped = append(res.Skipped, Skipped{Node: srv.Name, Reason: verr.Error()})
			continue
		}
		key := strings.ToLower(srv.Name)
		if seen[key] {
			res.Skipped = append(res.Skipped, Skipped{Node: srv.Name, Reason: "duplicate node name in the subscription"})
			continue
		}
		seen[key] = true
		res.Servers = append(res.Servers, *srv)
	}
	return res
}

// decodeBase64 decodes a subscription body in any of the base64 spellings a
// panel might use, ignoring embedded whitespace.
func decodeBase64(s string) (string, error) {
	s = strings.Join(strings.Fields(s), "")
	if s == "" {
		return "", errors.New("empty payload")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", errors.New("not base64")
}

// Fetch downloads a subscription with the v2ray user agent that makes panels
// return share links rather than a Clash page. It honours ctx and bounds the
// response size.
func Fetch(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("subscription: empty URL")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("subscription: %q is not a URL: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("subscription: only http(s) URLs are supported, got %q", u.Scheme)
	}
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("subscription: build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscription: fetch %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription: fetch %s: HTTP %d %s", u.Host, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("subscription: read %s: %w", u.Host, err)
	}
	if len(body) > MaxBody {
		return nil, fmt.Errorf("subscription: response from %s is larger than %d bytes", u.Host, MaxBody)
	}
	return body, nil
}

// DeriveProfileName turns a subscription URL into a profile name: the host,
// with a leading "www." dropped. It is what `import --url` uses when --profile
// is not given.
func DeriveProfileName(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return "sub"
	}
	host := u.Hostname()
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "sub"
	}
	return host
}

// clipLink returns a short, safe stand-in for a link that could not be parsed,
// so an error message never echoes credentials.
func clipLink(line string) string {
	if i := strings.Index(line, "://"); i >= 0 {
		scheme := line[:i]
		return scheme + "://…"
	}
	if len(line) > 40 {
		return line[:40] + "…"
	}
	return line
}
