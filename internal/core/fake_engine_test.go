package core

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The lifecycle test must not need sing-box, a network, or a real server. The
// cleanest fake is *this very test binary*: TestMain inspects the environment
// before running the tests, and when the fake marker is set it acts as a
// stand-in engine for the child invocation that core.Engine.Start spawns.
//
// The child is executed exactly like sing-box — `<binary> run -c <path>` — so
// the test exercises the real command line, the real generated config, and the
// real readiness probe.
const fakeEngineEnv = "VENDER_TEST_FAKE_ENGINE"

// Fake engine modes, passed through fakeEngineEnv.
const (
	fakeOK          = "ok"           // bind the inbound and stay alive
	fakeFail        = "fail"         // print an error and exit non-zero
	fakeNeverListen = "never-listen" // log "started" but never bind the port
	fakeExitAfter   = "exit-after:"  // bind, then exit after <ms>
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEngineEnv); mode != "" {
		os.Exit(fakeEngineMain(mode))
	}
	os.Exit(m.Run())
}

// fakeEngineMain emulates the parts of `sing-box run` the engine supervisor
// observes: a config file argument, output on stdout/stderr, a TCP listener on
// the mixed inbound, and a process that lives until it is signalled.
func fakeEngineMain(mode string) int {
	configPath := flagValue(os.Args[1:], "-c")
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
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "fake-sing-box: decode %s: %v\n", configPath, err)
		return 2
	}
	if len(cfg.Inbounds) == 0 {
		fmt.Fprintln(os.Stderr, "fake-sing-box: no inbound in the generated config")
		return 2
	}
	in := cfg.Inbounds[0]
	addr := net.JoinHostPort(in.Listen, strconv.Itoa(in.ListenPort))

	switch {
	case mode == fakeFail:
		fmt.Fprintln(os.Stderr, "fake-sing-box: FATAL decode config: deliberate failure")
		return 1
	case mode == fakeNeverListen:
		fmt.Fprintf(os.Stdout, "fake-sing-box: %s server started at %s\n", in.Type, addr)
		blockForever()
		return 0
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake-sing-box: listen %s: %v\n", addr, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "fake-sing-box: %s server started at %s\n", in.Type, addr)
	fmt.Fprintln(os.Stderr, "fake-sing-box: sing-box started (0.00s)")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	if strings.HasPrefix(mode, fakeExitAfter) {
		ms, err := strconv.Atoi(strings.TrimPrefix(mode, fakeExitAfter))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake-sing-box: bad exit-after value: %v\n", err)
			return 2
		}
		sleepMillis(ms)
		fmt.Fprintln(os.Stderr, "fake-sing-box: exiting on request")
		return 0
	}

	blockForever()
	return 0
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if rest, ok := strings.CutPrefix(a, name+"="); ok {
			return rest
		}
	}
	return ""
}

// blockForever parks the fake engine until a signal arrives. Go's default
// SIGTERM behaviour terminates the process, which is exactly what sing-box does
// and what Stop's graceful path relies on.
func blockForever() {
	select {}
}

func sleepMillis(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
