package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is vender's client for sing-box's experimental **Clash API**
// (sing-box `experimental.clash_api`). render.go emits the section; the code
// here consumes it.
//
// It exists because vender has no other way to learn two things the TUI needs:
//
//   - how fast the tunnel is moving (up/down bytes per second). Local counters
//     on the mixed inbound cannot see it: they would count the SOCKS/HTTP
//     framing, not the proxied payload, and they cannot see the engine's
//     connection reuse at all.
//   - how long the selected server actually takes, as observed *through the
//     tunnel* rather than as a bare TCP handshake to the server IP.
//
// The API is bound to loopback and always carries a secret (see render.go), so
// every request in this file authenticates. Endpoints used, all verified
// against sing-box 1.14.3:
//
//	GET /version                      -> {"version":"sing-box 1.14.3"}
//	GET /traffic                      -> {"up":N,"down":N} once per second, forever
//	GET /connections                  -> {"downloadTotal":N,"uploadTotal":N,...}
//	GET /proxies/<tag>/delay?...      -> {"delay":N} milliseconds
//
// Authentication is the `Authorization: Bearer <secret>` header. sing-box
// 1.14.3 rejects `?token=` ("Unauthorized"), so the header is the only form
// this client uses.

// DefaultDelayTestURL is the URL the latency probe fetches through the tunnel.
// It is the conventional "generate 204" endpoint used by Clash clients: tiny,
// cacheable and reachable from anywhere the proxy works.
const DefaultDelayTestURL = "http://www.gstatic.com/generate_204"

// DefaultDelayTimeout bounds one latency probe.
const DefaultDelayTimeout = 5 * time.Second

// ErrClashDisabled is returned when the client was built without an endpoint,
// i.e. the running engine has no Clash API.
var ErrClashDisabled = errors.New("core: clash api is not enabled")

// ClashTotals is the cumulative byte counter reported by /connections.
type ClashTotals struct {
	Download int64
	Upload   int64
}

// ClashClient is a minimal client for the subset of the Clash API vender uses.
// It is safe for concurrent use.
type ClashClient struct {
	base   string
	secret string
	hc     *http.Client
	// requestTimeout bounds a single request/response call. There is
	// deliberately no client-level timeout: /traffic is a stream that stays
	// open for as long as the engine lives, and a whole-request deadline would
	// tear it down every few seconds.
	requestTimeout time.Duration
}

// NewClashClient returns a client for the Clash API listening on listen
// ("host:port"). An empty listen yields a client whose every call fails with
// ErrClashDisabled, so callers can keep one code path whether or not the API
// is enabled.
func NewClashClient(listen, secret string) *ClashClient {
	c := &ClashClient{secret: secret, requestTimeout: 10 * time.Second}
	if listen != "" {
		c.base = "http://" + listen
	}
	// The API is a loopback Unix-domain-speed service, and there must be no
	// proxy from the environment (an inherited http_proxy would send these
	// requests to the very proxy being measured).
	c.hc = &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConnsPerHost: 4,
		},
	}
	return c
}

// Enabled reports whether the client has an endpoint to talk to.
func (c *ClashClient) Enabled() bool { return c != nil && c.base != "" }

// Version returns the engine's self-reported version, e.g. "sing-box 1.14.3".
func (c *ClashClient) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/version", &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// Totals returns the cumulative download/upload byte counters.
func (c *ClashClient) Totals(ctx context.Context) (ClashTotals, error) {
	var out struct {
		DownloadTotal int64 `json:"downloadTotal"`
		UploadTotal   int64 `json:"uploadTotal"`
	}
	if err := c.getJSON(ctx, "/connections", &out); err != nil {
		return ClashTotals{}, err
	}
	return ClashTotals{Download: out.DownloadTotal, Upload: out.UploadTotal}, nil
}

// Delay measures how long a round trip through the named outbound takes, in
// milliseconds, by asking the engine to fetch probeURL through it.
//
// This measures the tunnel, not the server: a server whose TCP handshake is
// fast but which cannot carry traffic reports a failure here, which is exactly
// what a user switching servers wants to know.
func (c *ClashClient) Delay(ctx context.Context, outbound, probeURL string, timeout time.Duration) (int, error) {
	if outbound == "" {
		outbound = "proxy"
	}
	if probeURL == "" {
		probeURL = DefaultDelayTestURL
	}
	if timeout <= 0 {
		timeout = DefaultDelayTimeout
	}
	// The engine stops waiting at `timeout`; the request deadline adds a
	// margin so the engine's own "no delay" answer wins the race.
	cctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()

	q := url.Values{}
	q.Set("timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	q.Set("url", probeURL)
	path := "/proxies/" + url.PathEscape(outbound) + "/delay?" + q.Encode()

	body, status, err := c.get(cctx, path)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, clashError(status, body)
	}
	var out struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("core: clash api delay: %w", err)
	}
	if out.Delay <= 0 {
		return 0, errors.New("core: clash api reported no delay")
	}
	return out.Delay, nil
}

// TrafficStream reads the /traffic stream, calling fn once per sample, until
// the context is cancelled, the stream ends, or fn returns an error. Rates are
// bytes per second, as reported by the engine.
//
// fn is called from the calling goroutine; it must not block for long.
func (c *ClashClient) TrafficStream(ctx context.Context, fn func(up, down float64) error) error {
	if !c.Enabled() {
		return ErrClashDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/traffic", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return clashError(resp.StatusCode, body)
	}

	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var sample struct {
			Up   float64 `json:"up"`
			Down float64 `json:"down"`
		}
		if err := dec.Decode(&sample); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return fmt.Errorf("core: clash api traffic: %w", err)
		}
		if err := fn(sample.Up, sample.Down); err != nil {
			return err
		}
	}
}

// --- plumbing --------------------------------------------------------------

func (c *ClashClient) getJSON(ctx context.Context, path string, out any) error {
	cctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	body, status, err := c.get(cctx, path)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return clashError(status, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("core: clash api %s: %w", path, err)
	}
	return nil
}

func (c *ClashClient) get(ctx context.Context, path string) ([]byte, int, error) {
	if !c.Enabled() {
		return nil, 0, ErrClashDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// clashError turns a non-200 response into an error. A 401 is called out
// explicitly: it almost always means the daemon's secret and the running
// engine's secret have drifted apart (a stale singbox.json), not that the
// user mistyped anything.
func clashError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("core: clash api rejected the request (HTTP %d): the running engine's secret does not match", status)
	}
	return fmt.Errorf("core: clash api error (HTTP %d): %s", status, msg)
}
