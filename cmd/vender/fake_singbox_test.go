package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A full daemon integration test must not need sing-box, a network, or a real
// server. The fake is this very test binary: TestMain inspects the environment
// before running the tests, and when the marker is set it behaves like
// `sing-box run -c <config>` for the child invocation the daemon spawns.
//
// Unlike internal/core's fake (which only needs to satisfy the supervisor),
// this one also impersonates the experimental Clash API, because the M3
// control channel proxies live traffic and latency from it. That makes the
// whole daemon -> engine -> Clash API -> control channel -> client path
// exercisable with no external process and no privileges.
//
// The fake is a faithful-enough engine, not a proxy: it never dials anything.
const fakeEngineEnv = "VENDER_TEST_DAEMON_FAKE_ENGINE"

// The fake reports fixed numbers so tests can assert on them.
const (
	fakeClashVersion = "sing-box 1.14.3-fake"
	fakeDelayMs      = 42
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEngineEnv); mode != "" {
		os.Exit(fakeEngineMain(mode))
	}
	os.Exit(m.Run())
}

func fakeEngineMain(mode string) int {
	configPath := ""
	args := os.Args[1:]
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			configPath = args[i+1]
		}
	}
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "fake-sing-box: no -c <config> argument")
		return 2
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-sing-box: read %s: %v\n", configPath, err)
		return 2
	}
	var cfg struct {
		Inbounds []struct {
			Type       string `json:"type"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "fake-sing-box: decode %s: %v\n", configPath, err)
		return 2
	}
	if len(args) > 0 && args[0] == "check" {
		return 0
	}
	if len(cfg.Inbounds) == 0 {
		fmt.Fprintln(os.Stderr, "fake-sing-box: no inbound in the generated config")
		return 2
	}
	in := cfg.Inbounds[0]
	addr := net.JoinHostPort(in.Listen, strconv.Itoa(in.ListenPort))

	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "fake-sing-box: FATAL decode config: deliberate failure")
		return 1
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-sing-box: listen %s: %v\n", addr, err)
		return 1
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	fmt.Fprintf(os.Stdout, "%s server started at %s\n", in.Type, addr)

	api := cfg.Experimental.ClashAPI
	if api.ExternalController != "" {
		clashLn, err := net.Listen("tcp", api.ExternalController)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-sing-box: clash api listen %s: %v\n", api.ExternalController, err)
			return 1
		}
		defer clashLn.Close()
		go serveFakeClash(clashLn, api.Secret)
		fmt.Fprintf(os.Stdout, "clash-api: restful api listening at %s\n", api.ExternalController)
	}

	fmt.Fprintln(os.Stderr, "sing-box started (0.00s)")
	select {} // run until signalled
}

// serveFakeClash implements just enough of the Clash API for the daemon:
// /version, /connections, /proxies/<tag>/delay and the /traffic stream.
func serveFakeClash(ln net.Listener, secret string) {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/version"):
			_, _ = w.Write([]byte(`{"version":"` + fakeClashVersion + `"}`))
		case strings.HasPrefix(r.URL.Path, "/connections"):
			_, _ = w.Write([]byte(`{"connections":[],"downloadTotal":88000,"uploadTotal":44000}`))
		case strings.Contains(r.URL.Path, "/delay"):
			_, _ = fmt.Fprintf(w, `{"delay":%d}`, fakeDelayMs)
		case strings.HasPrefix(r.URL.Path, "/traffic"):
			fl, _ := w.(http.Flusher)
			for i := 0; i < 600; i++ {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"up":%d,"down":%d}`+"\n", 1000*(i+1), 2048*(i+1))))
				if fl != nil {
					fl.Flush()
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	_ = srv.Serve(ln)
}
