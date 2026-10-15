package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ll-lite/vender/internal/config"
)

// The Clash API client is tested against an httptest server that impersonates
// the subset of sing-box 1.14.3 vender uses (and, importantly, its auth: a
// request without the bearer token is rejected, like the real one).
func newClashTestServer(t *testing.T, secret string) (*httptest.Server, *ClashClient) {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return false
		}
		return true
	}
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		_, _ = w.Write([]byte(`{"meta":true,"premium":true,"version":"sing-box 1.14.3"}`))
	})
	mux.HandleFunc("/connections", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		_, _ = w.Write([]byte(`{"connections":[],"downloadTotal":12407,"uploadTotal":3858,"memory":8126464}`))
	})
	mux.HandleFunc("/proxies/proxy/delay", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if r.URL.Query().Get("timeout") == "" || r.URL.Query().Get("url") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"delay":924}`))
	})
	mux.HandleFunc("/traffic", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		fl, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte(`{"up":100,"down":2048}` + "\n"))
			if fl != nil {
				fl.Flush()
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, NewClashClient(srv.Listener.Addr().String(), secret)
}

func TestClashClientVersionTotalsDelay(t *testing.T) {
	_, c := newClashTestServer(t, "sekrit")
	ctx := context.Background()

	got, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if got != "sing-box 1.14.3" {
		t.Errorf("Version = %q", got)
	}

	totals, err := c.Totals(ctx)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Download != 12407 || totals.Upload != 3858 {
		t.Errorf("Totals = %+v", totals)
	}

	ms, err := c.Delay(ctx, "proxy", "http://example.com/generate_204", time.Second)
	if err != nil {
		t.Fatalf("Delay: %v", err)
	}
	if ms != 924 {
		t.Errorf("Delay = %d, want 924", ms)
	}
}

func TestClashClientTrafficStream(t *testing.T) {
	_, c := newClashTestServer(t, "sekrit")

	var samples []float64
	err := c.TrafficStream(context.Background(), func(up, down float64) error {
		samples = append(samples, down)
		return nil
	})
	// The test server ends the stream, which surfaces as io.EOF.
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("TrafficStream: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(samples))
	}
	for _, s := range samples {
		if s != 2048 {
			t.Errorf("sample = %v, want 2048", s)
		}
	}
}

func TestClashClientWrongSecretExplainsItself(t *testing.T) {
	srv, _ := newClashTestServer(t, "sekrit")
	wrong := NewClashClient(srv.Listener.Addr().String(), "not-the-secret")

	_, err := wrong.Version(context.Background())
	if err == nil {
		t.Fatal("Version with the wrong secret = nil error")
	}
	if !strings.Contains(err.Error(), "secret does not match") {
		t.Errorf("error = %q, want it to explain the secret mismatch", err)
	}
}

func TestClashClientDisabled(t *testing.T) {
	c := NewClashClient("", "")
	if c.Enabled() {
		t.Error("Enabled() = true for a client with no endpoint")
	}
	if _, err := c.Version(context.Background()); !errors.Is(err, ErrClashDisabled) {
		t.Errorf("Version = %v, want ErrClashDisabled", err)
	}
	if err := c.TrafficStream(context.Background(), func(float64, float64) error { return nil }); !errors.Is(err, ErrClashDisabled) {
		t.Errorf("TrafficStream = %v, want ErrClashDisabled", err)
	}
}

func TestClashListenValidation(t *testing.T) {
	base := *config.Default()
	valid := RuntimeConfig{
		Config: base, Server: testVLESS(), RunDir: "/run/vender",
		ClashListen: "127.0.0.1:9090", ClashSecret: "s",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("loopback clash listen rejected: %v", err)
	}

	bad := []struct {
		name   string
		listen string
		secret string
		want   string
	}{
		{"wildcard", "0.0.0.0:9090", "s", "loopback"},
		{"lan", "192.168.1.5:9090", "s", "loopback"},
		{"hostname", "example.com:9090", "s", "loopback"},
		{"no port", "127.0.0.1", "s", "host:port"},
		{"no secret", "127.0.0.1:9090", "", "ClashSecret"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			rc := valid
			rc.ClashListen, rc.ClashSecret = tt.listen, tt.secret
			err := rc.Validate()
			if err == nil {
				t.Fatalf("Validate(%q) = nil error", tt.listen)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}
