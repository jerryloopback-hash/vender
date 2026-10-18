package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ll-lite/vender/internal/config"
	"github.com/ll-lite/vender/internal/core"
	"github.com/ll-lite/vender/internal/rules"
)

// Defaults for the controller's internal buffers and cadence.
const (
	// logRingSize is how many recent engine lines a newly attached client
	// receives immediately. Without it, attaching the TUI to a daemon that has
	// been up for hours would show an empty Logs screen.
	logRingSize = 250
	// subBuffer is the per-subscriber buffer. A subscriber that stops reading
	// loses samples rather than blocking the daemon — the same policy core
	// applies to engine output.
	subBuffer = 512
	// totalsEvery is how often (in traffic samples, i.e. seconds) the
	// cumulative counters are refreshed. They are cosmetic; the rates are not.
	totalsEvery = 5
	// trafficMinBackoff/trafficMaxBackoff bound the reconnect delay when the
	// engine's Clash API is not (yet) answering.
	trafficMinBackoff = 500 * time.Millisecond
	trafficMaxBackoff = 10 * time.Second
	// Bound crash loops across successful-but-short-lived restarts as well as
	// failed starts. A manual lifecycle command starts a new recovery budget.
	recoveryLimit  = 5
	recoveryWindow = 5 * time.Minute
)

// ControllerOptions configures a Controller.
type ControllerOptions struct {
	// Probe defaults to core.DirectPing; injectable for hermetic embedding/tests.
	Probe core.PingProbe
	// Store reads and writes config.yaml / servers.yaml.
	Store *config.Store
	// RunDir is the daemon's runtime directory.
	RunDir string
	// Engine is the supervised proxy engine. Tests pass a fake.
	Engine core.Engine
	// BinaryPath is the resolved sing-box executable, needed to re-render the
	// runtime configuration on every restart.
	BinaryPath string
	// Version is the vender build string.
	Version string
	// ClashListen overrides the Clash API address. Empty means "allocate a
	// free loopback port".
	ClashListen string
	// ClashSecret overrides the Clash API token. Empty means "generate one".
	ClashSecret string
	// Logf, when set, receives daemon-side diagnostics (never credentials).
	Logf func(format string, args ...any)
}

// Controller is the daemon's brain: it owns the engine, the rendered runtime
// configuration and the server list, and it serves the control protocol.
//
// It implements Handler, so Server and Controller are wired together with
// nothing in between:
//
//	ctrl := control.NewController(...)
//	srv  := control.NewServer(control.ServerOptions{Handler: ctrl, RunDir: runDir})
//	go srv.Serve()
//
// Concurrency: mutating engine operations are serialised by ops, so two
// clients cannot restart the engine on top of each other; every field they
// touch is guarded by mu, and each subscription is a buffered channel that is
// dropped from rather than blocked on. The result is that a slow TUI can never
// stall the daemon, and a request that takes seconds (a restart) does not stop
// other clients from reading status.
type Controller struct {
	probe      core.PingProbe
	store      *config.Store
	runDir     string
	engine     core.Engine
	binary     string
	version    string
	logf       func(format string, args ...any)
	clash      *core.ClashClient
	clashAddr  string
	clashToken string

	ops sync.Mutex // serialises engine lifecycle changes

	mu               sync.RWMutex
	cfg              config.Config
	servers          *config.ServerSet
	profiles         *config.ProfileSet
	running          bool
	restarting       bool
	stopping         bool
	lastErr          string
	startedAt        time.Time
	enginePid        int
	engVersion       string
	gen              int
	lastRuntime      core.RuntimeConfig
	wanted           bool
	recoveryEpoch    uint64
	recoveryAttempts []time.Time
	recoveryDelay    time.Duration

	logMu   sync.Mutex
	logSubs map[int]chan any
	logRing []LogLine
	logIDs  int

	trafMu    sync.Mutex
	trafSubs  map[int]chan any
	trafIDs   int
	trafStop  chan struct{}
	trafGen   int
	trafTotal core.ClashTotals

	closeOnce sync.Once
	closed    chan struct{}
}

// NewController builds the controller and renders the initial engine
// configuration (but does not start the engine; call StartEngine).
func NewController(opts ControllerOptions) (*Controller, error) {
	if opts.Store == nil {
		return nil, errors.New("control: ControllerOptions.Store is required")
	}
	if opts.Engine == nil {
		return nil, errors.New("control: ControllerOptions.Engine is required")
	}
	if opts.RunDir == "" {
		return nil, errors.New("control: ControllerOptions.RunDir is required")
	}

	addr := opts.ClashListen
	if addr == "" {
		var err error
		addr, err = allocateLoopbackAddr()
		if err != nil {
			return nil, err
		}
	}
	token := opts.ClashSecret
	if token == "" {
		var err error
		if token, err = randomToken(); err != nil {
			return nil, err
		}
	}

	if opts.Probe == nil {
		opts.Probe = core.DirectPing
	}
	c := &Controller{
		probe:         opts.Probe,
		store:         opts.Store,
		runDir:        opts.RunDir,
		engine:        opts.Engine,
		binary:        opts.BinaryPath,
		version:       opts.Version,
		logf:          opts.Logf,
		clash:         core.NewClashClient(addr, token),
		clashAddr:     addr,
		clashToken:    token,
		logSubs:       make(map[int]chan any),
		trafSubs:      make(map[int]chan any),
		closed:        make(chan struct{}),
		recoveryDelay: time.Second,
	}

	// Render (but do not start) so a broken configuration or a missing server
	// is reported before the control channel is published.
	if _, err := c.render(); err != nil {
		return nil, err
	}
	return c, nil
}

// RunDir returns the daemon's runtime directory.
func (c *Controller) RunDir() string { return c.runDir }

// ClashListen returns the Clash API address the engine was configured with.
func (c *Controller) ClashListen() string { return c.clashAddr }

// StartEngine renders the current configuration and starts the engine.
func (c *Controller) StartEngine() error {
	c.ops.Lock()
	defer c.ops.Unlock()
	c.setWanted(true)
	return c.startLocked()
}

// StopEngine stops the engine, leaving the daemon (and the control channel)
// running.
func (c *Controller) StopEngine() error {
	c.ops.Lock()
	defer c.ops.Unlock()
	c.setWanted(false)
	return c.stopLocked()
}

// RestartEngine stops and starts the engine, re-rendering the configuration
// from the files on disk first, so a server imported from another terminal is
// picked up by a restart.
func (c *Controller) RestartEngine() error {
	c.ops.Lock()
	defer c.ops.Unlock()
	return c.restartLocked()
}

// setWanted is called with ops held. Incrementing the epoch cancels pending
// recovery without waiting for its backoff timer.
func (c *Controller) setWanted(wanted bool) {
	c.mu.Lock()
	c.wanted = wanted
	c.recoveryEpoch++
	c.recoveryAttempts = nil
	c.mu.Unlock()
}

// SelectServer makes name the active server, persists the choice to
// config.yaml, and restarts the engine if it is running.
func (c *Controller) SelectServer(name string) error {
	c.ops.Lock()
	defer c.ops.Unlock()

	if strings.TrimSpace(name) == "" {
		return Errorf(KindBadRequest, "control: select needs a server name")
	}
	if err := c.store.SelectServer(name); err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return Errorf(KindNotFound, "control: %v", err)
		}
		return Errorf(KindInternal, "control: %v", err)
	}
	return c.applyLocked()
}

// SetProfile makes a profile active, persists it to profiles.yaml, and
// restarts the engine if it is running. With selected_server "auto" that
// changes which nodes the failover group may use, so a running engine has to
// be re-rendered — hence the same render-and-restart path as a mode switch.
//
// An unknown name is refused before anything is written, so a typo cannot
// leave the config pointing at a profile that does not exist.
func (c *Controller) SetProfile(name string) error {
	c.ops.Lock()
	defer c.ops.Unlock()

	ps, err := c.store.LoadProfiles()
	if err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	ss, err := c.store.LoadServers()
	if err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	canonical, ok := config.KnownProfileName(ss, ps, name)
	if !ok {
		return Errorf(KindNotFound, "control: no profile named %q (see `vender profiles list`)", name)
	}
	if canonical == config.ProfileAll {
		ps.Active = config.ProfileAll
	} else if strings.EqualFold(canonical, config.ProfileManual) {
		ps.Active = ""
	} else {
		ps.Active = canonical
	}
	if err := c.store.SaveProfiles(ps); err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	c.note(fmt.Sprintf("profile %s", ps.ActiveName()))
	return c.applyLocked()
}

// SetTun turns the TUN inbound on or off, persists the choice to config.yaml,
// and restarts the engine if it is running. Toggling TUN is a bigger change
// than a mode switch — the engine's whole inbound set changes — so it goes
// through the same render-and-restart path and reports the engine's own error
// (a missing CAP_NET_ADMIN, say) to the caller.
func (c *Controller) SetTun(enabled bool) error {
	c.ops.Lock()
	defer c.ops.Unlock()

	cfg, err := c.store.LoadConfig()
	if err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	if cfg.Engine.Tun.Enabled == enabled {
		// Nothing to change; still answer, but do not blink the engine.
		return nil
	}
	cfg.Engine.Tun.Enabled = enabled
	if err := cfg.Validate(); err != nil {
		return Errorf(KindBadRequest, "control: %v", err)
	}
	if enabled {
		// Refuse before touching the engine when this daemon cannot do it: the
		// alternative is a restart that stops a working proxy and then fails,
		// leaving the user without either.
		if perr := core.CheckTUNPrivilege(c.binary).Err(); perr != nil {
			return Errorf(KindBadRequest, "%v", perr)
		}
	}
	if err := c.store.SaveConfig(cfg); err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	c.note(fmt.Sprintf("tun %s", onOff(enabled)))
	return c.applyLocked()
}

// SetMode changes the routing mode, persists it, and restarts the engine if it
// is running.
func (c *Controller) SetMode(mode string) error {
	c.ops.Lock()
	defer c.ops.Unlock()

	m := config.ProxyMode(strings.ToLower(strings.TrimSpace(mode)))
	if !m.Valid() {
		return Errorf(KindBadRequest, "control: mode %q is not one of rule, global, direct", mode)
	}
	cfg, err := c.store.LoadConfig()
	if err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	if cfg.Proxy.Mode == m {
		// Nothing to change: still return the current status, but do not drop
		// the connection for a no-op.
		return nil
	}
	cfg.Proxy.Mode = m
	if err := cfg.Validate(); err != nil {
		return Errorf(KindBadRequest, "control: %v", err)
	}
	if err := c.store.SaveConfig(cfg); err != nil {
		return Errorf(KindInternal, "control: %v", err)
	}
	return c.applyLocked()
}

// applyLocked re-renders the configuration from disk and restarts the engine
// when it is running. When it is not, the config is only re-rendered, so a
// later StartEngine uses the new choice without a pointless restart.
func (c *Controller) applyLocked() error {
	c.mu.RLock()
	running := c.running
	c.mu.RUnlock()
	if !running {
		_, err := c.render()
		return err
	}
	return c.restartLocked()
}

// Close stops the engine and every internal goroutine. It is idempotent.
func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.ops.Lock()
		c.setWanted(false)
		_ = c.stopLocked()
		c.ops.Unlock()
	})
}

// --- engine lifecycle (ops held) -------------------------------------------

func (c *Controller) startLocked() error {
	c.mu.RLock()
	already := c.running
	c.mu.RUnlock()
	if already {
		return nil
	}
	rc, servers, profiles, err := c.loadRuntime()
	if err != nil {
		c.fail(err)
		return err
	}
	return c.startRuntimeLocked(rc, servers, profiles)
}

func (c *Controller) startRuntimeLocked(rc core.RuntimeConfig, servers *config.ServerSet, profiles *config.ProfileSet) error {
	if err := c.engine.WriteConfig(rc); err != nil {
		c.fail(err)
		return err
	}

	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.stopping = false
	c.restarting = false
	c.mu.Unlock()

	if err := c.engine.Start(); err != nil {
		c.fail(err)
		return err
	}

	c.mu.Lock()
	c.running = true
	c.cfg = rc.Config
	c.servers = servers
	c.profiles = profiles
	c.lastRuntime = rc
	c.startedAt = time.Now()
	c.lastErr = ""
	c.enginePid = enginePID(c.engine)
	c.mu.Unlock()

	c.watchLogs(gen)
	c.startTraffic(gen)
	c.note(fmt.Sprintf("engine started: %s (listen %s)", rc.Config.Proxy.Mode, rc.Config.Proxy.Listen))
	return nil
}

func (c *Controller) stopLocked() error {
	c.mu.Lock()
	wasRunning := c.running
	c.stopping = true
	c.mu.Unlock()
	if !wasRunning {
		return nil
	}
	c.stopTraffic()
	err := c.engine.Stop()
	c.mu.Lock()
	c.running = false
	c.enginePid = 0
	c.stopping = false
	c.mu.Unlock()
	c.note("engine stopped")
	if err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Controller) restartLocked() error {
	// Refuse an invalid candidate before dropping a working proxy. render also
	// runs the pure renderer, catching errors that RuntimeConfig.Validate alone
	// cannot catch (for example outbound tag collisions).
	rc, servers, profiles, err := c.loadRuntime()
	if err != nil {
		c.fail(err)
		return err
	}
	if checker, ok := c.engine.(interface {
		CheckConfig(core.RuntimeConfig) error
	}); ok {
		if err := checker.CheckConfig(rc); err != nil {
			c.fail(err)
			return err
		}
	}
	c.mu.RLock()
	previous, previousServers, previousProfiles, wasRunning := c.lastRuntime, c.servers, c.profiles, c.running
	c.mu.RUnlock()
	c.setWanted(true)
	c.mu.Lock()
	c.restarting = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.restarting = false
		c.mu.Unlock()
	}()
	if err := c.stopLocked(); err != nil {
		return err
	}
	if err := c.startRuntimeLocked(rc, servers, profiles); err != nil {
		if wasRunning {
			if restoreErr := c.startRuntimeLocked(previous, previousServers, previousProfiles); restoreErr != nil {
				err = fmt.Errorf("apply failed: %w; restoring previous engine also failed: %v", err, restoreErr)
			} else {
				err = fmt.Errorf("apply failed: %w; previous engine restored (disk configuration remains pending)", err)
			}
		}
		c.fail(err)
		return err
	}
	return nil
}

// render builds the RuntimeConfig from the files on disk and refreshes the
// controller's snapshot of them. Reading config.yaml and servers.yaml on every
// render is what makes `vender import` in another terminal take effect on the
// next restart.
func (c *Controller) render() (core.RuntimeConfig, error) {
	rc, servers, profiles, err := c.loadRuntime()
	if err != nil {
		return core.RuntimeConfig{}, err
	}
	c.mu.Lock()
	c.cfg = rc.Config
	c.servers = servers
	c.profiles = profiles
	c.mu.Unlock()
	return rc, nil
}

// Loading/validating a candidate must not publish it as applied state. A failed
// switch can leave newer settings on disk while the old engine keeps working.
func (c *Controller) loadRuntime() (core.RuntimeConfig, *config.ServerSet, *config.ProfileSet, error) {
	cfg, err := c.store.LoadConfig()
	if err != nil {
		return core.RuntimeConfig{}, nil, nil, Errorf(KindInternal, "control: %v", err)
	}
	servers, err := c.store.LoadServers()
	if err != nil {
		return core.RuntimeConfig{}, nil, nil, Errorf(KindInternal, "control: %v", err)
	}
	profiles, err := c.store.LoadProfiles()
	if err != nil {
		return core.RuntimeConfig{}, nil, nil, Errorf(KindInternal, "control: %v", err)
	}

	var srv *config.Server
	var all []config.Server
	if cfg.Proxy.Mode != config.ModeDirect {
		s, as, serr := cfg.SelectionInProfile(servers, profiles.ActiveName())
		if serr != nil {
			kind := KindBadRequest
			if errors.Is(serr, config.ErrNotFound) {
				kind = KindNotFound
			}
			return core.RuntimeConfig{}, nil, nil, Errorf(kind, "control: %v", serr)
		}
		srv, all = s, as
	}

	userRules, err := rules.LoadUserRules(c.store.RulesPath())
	if err != nil {
		return core.RuntimeConfig{}, nil, nil, Errorf(KindBadRequest, "control: %v", err)
	}

	rc := core.RuntimeConfig{
		Config:      *cfg,
		Server:      srv,
		Servers:     all,
		UserRules:   userRules,
		ProfileDNS:  profiles.DNSMap(),
		BinaryPath:  c.binary,
		RunDir:      c.runDir,
		ClashListen: c.clashAddr,
		ClashSecret: c.clashToken,
	}
	if err := rc.Validate(); err != nil {
		return core.RuntimeConfig{}, nil, nil, Errorf(KindInternal, "control: %v", err)
	}
	if _, err := core.BuildConfig(rc); err != nil {
		return core.RuntimeConfig{}, nil, nil, Errorf(KindBadRequest, "control: %v", err)
	}
	return rc, servers, profiles, nil
}

func (c *Controller) fail(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
	c.note("engine error: " + err.Error())
}

func (c *Controller) note(msg string) {
	c.publishLog(LogLine{Line: msg, Time: time.Now().Format("15:04:05"), Source: SourceDaemon})
}

// watchLogs forwards one engine run's output to the subscribers. The log
// channel of a run is only available once the process exists, and a restart
// installs a fresh channel, so this is called after every successful start.
//
// When the channel closes, the run has ended. If that was not requested (a
// crash, or sing-box exiting on its own) the controller records it so `status`
// stops claiming the engine is up.
func (c *Controller) watchLogs(gen int) {
	ch := c.engine.Logs()
	go func() {
		for line := range ch {
			c.publishLog(LogLine{Line: line, Time: time.Now().Format("15:04:05"), Source: SourceEngine})
		}
		c.mu.Lock()
		unexpected := false
		epoch := c.recoveryEpoch
		if c.gen == gen {
			c.running = false
			c.enginePid = 0
			if !c.stopping && !c.restarting {
				c.lastErr = "engine exited"
				unexpected = c.wanted
			}
		}
		c.mu.Unlock()
		c.stopTrafficGen(gen)
		if unexpected {
			go c.recoverEngine(epoch)
		}
	}()
}

// recoverEngine restores only unexpected exits, with a rolling crash budget.
// ops and the epoch check prevent a pending recovery from undoing a user's
// stop/restart or resurrecting the engine after Close.
func (c *Controller) recoverEngine(epoch uint64) {
	for attempt := 0; ; attempt++ {
		delay := c.recoveryDelay * time.Duration(1<<min(attempt, 4))
		timer := time.NewTimer(delay)
		select {
		case <-c.closed:
			timer.Stop()
			return
		case <-timer.C:
		}
		c.ops.Lock()
		c.mu.Lock()
		if !c.wanted || c.recoveryEpoch != epoch || c.running {
			c.mu.Unlock()
			c.ops.Unlock()
			return
		}
		select {
		case <-c.closed:
			c.mu.Unlock()
			c.ops.Unlock()
			return
		default:
		}
		now := time.Now()
		kept := c.recoveryAttempts[:0]
		for _, when := range c.recoveryAttempts {
			if now.Sub(when) < recoveryWindow {
				kept = append(kept, when)
			}
		}
		c.recoveryAttempts = kept
		if len(kept) >= recoveryLimit {
			c.lastErr = "engine recovery limit reached (5 attempts in 5 minutes); start manually after correcting the failure"
			c.mu.Unlock()
			c.ops.Unlock()
			c.note("engine recovery limit reached")
			return
		}
		c.recoveryAttempts = append(c.recoveryAttempts, now)
		rc, servers, profiles := c.lastRuntime, c.servers, c.profiles
		c.mu.Unlock()
		c.note("recovering unexpectedly exited engine")
		// Recover the last applied configuration, not unvalidated edits that
		// another process may have left on disk since the engine started.
		err := c.startRuntimeLocked(rc, servers, profiles)
		c.ops.Unlock()
		if err == nil {
			return
		}
	}
}

// --- traffic ----------------------------------------------------------------

func (c *Controller) startTraffic(gen int) {
	if !c.clash.Enabled() {
		return
	}
	stop := make(chan struct{})
	c.trafMu.Lock()
	if c.trafStop != nil {
		close(c.trafStop)
	}
	c.trafStop = stop
	c.trafGen = gen
	c.trafMu.Unlock()

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-c.closed:
				cancel()
			case <-ctx.Done():
			}
		}()
		c.trafficLoop(ctx)
	}()
}

// stopTraffic ends the poller unconditionally (Stop/Close).
func (c *Controller) stopTraffic() {
	c.trafMu.Lock()
	stop := c.trafStop
	c.trafStop = nil
	c.trafMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// stopTrafficGen ends the poller only if it belongs to run gen. It is what the
// per-run log watcher calls when its run ends: a restart may already have
// installed a fresh poller, and closing that one would silently kill live
// traffic reporting for the new run.
func (c *Controller) stopTrafficGen(gen int) {
	c.trafMu.Lock()
	var stop chan struct{}
	if c.trafGen == gen {
		stop = c.trafStop
		c.trafStop = nil
	}
	c.trafMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// trafficLoop follows the engine's /traffic stream, reconnecting while the
// engine is up. Rate samples are published as they arrive; the cumulative
// counters are refreshed every few seconds.
func (c *Controller) trafficLoop(ctx context.Context) {
	backoff := trafficMinBackoff
	n := 0
	for ctx.Err() == nil {
		err := c.clash.TrafficStream(ctx, func(up, down float64) error {
			backoff = trafficMinBackoff
			n++
			if n == 1 || n%totalsEvery == 0 {
				if totals, terr := c.clash.Totals(ctx); terr == nil {
					c.trafMu.Lock()
					c.trafTotal = totals
					c.trafMu.Unlock()
				}
			}
			if n == 1 {
				if v, verr := c.clash.Version(ctx); verr == nil {
					c.mu.Lock()
					c.engVersion = v
					c.mu.Unlock()
				}
			}
			c.trafMu.Lock()
			totals := c.trafTotal
			c.trafMu.Unlock()
			c.publishTraffic(TrafficSample{Up: up, Down: down, TotalUp: totals.Upload, TotalDown: totals.Download})
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) && c.logf != nil {
			// A reconnect is normal while the engine restarts; a genuine
			// failure is worth a line on the daemon's stdout.
			c.logf("traffic stream interrupted: %v", err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < trafficMaxBackoff {
			backoff *= 2
		}
	}
}

// --- publications -----------------------------------------------------------

func (c *Controller) publishLog(ll LogLine) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logRing = append(c.logRing, ll)
	if len(c.logRing) > logRingSize {
		c.logRing = append(c.logRing[:0], c.logRing[len(c.logRing)-logRingSize:]...)
	}
	for _, ch := range c.logSubs {
		select {
		case ch <- ll:
		default: // slow subscriber: drop rather than block the daemon
		}
	}
}

func (c *Controller) publishTraffic(s TrafficSample) {
	c.trafMu.Lock()
	defer c.trafMu.Unlock()
	for _, ch := range c.trafSubs {
		select {
		case ch <- s:
		default:
		}
	}
}

// Subscribe implements Handler.
func (c *Controller) Subscribe(name string) (<-chan any, func(), error) {
	switch name {
	case StreamLogs:
		return c.subscribeLogs()
	case StreamTraffic:
		return c.subscribeTraffic()
	default:
		return nil, nil, Errorf(KindNotFound, "control: unknown stream %q", name)
	}
}

func (c *Controller) subscribeLogs() (<-chan any, func(), error) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	id := c.logIDs
	c.logIDs++
	ch := make(chan any, subBuffer)
	for _, ll := range c.logRing { // replay recent history to a late attach
		ch <- ll
	}
	c.logSubs[id] = ch
	return ch, func() { c.removeLogSub(id) }, nil
}

func (c *Controller) removeLogSub(id int) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	if ch, ok := c.logSubs[id]; ok {
		delete(c.logSubs, id)
		close(ch)
	}
}

func (c *Controller) subscribeTraffic() (<-chan any, func(), error) {
	if !c.clash.Enabled() {
		return nil, nil, Errorf(KindNotFound, "control: the engine has no traffic API enabled")
	}
	c.trafMu.Lock()
	defer c.trafMu.Unlock()
	id := c.trafIDs
	c.trafIDs++
	ch := make(chan any, subBuffer)
	c.trafSubs[id] = ch
	return ch, func() { c.removeTrafSub(id) }, nil
}

func (c *Controller) removeTrafSub(id int) {
	c.trafMu.Lock()
	defer c.trafMu.Unlock()
	if ch, ok := c.trafSubs[id]; ok {
		delete(c.trafSubs, id)
		close(ch)
	}
}

// --- queries ----------------------------------------------------------------

// Status implements Handler and CmdStatus: a snapshot of what the daemon is
// doing right now.
func (c *Controller) Status() StatusData {
	c.mu.RLock()
	cfg := c.cfg
	servers := c.servers
	running := c.running
	restarting := c.restarting
	lastErr := c.lastErr
	startedAt := c.startedAt
	pid := c.enginePid
	engVersion := c.engVersion
	profiles := c.profiles
	c.mu.RUnlock()

	priv := core.CheckTUNPrivilege(c.binary)
	tun := TunStatus{
		Enabled:         cfg.Engine.Tun.Enabled,
		Active:          running && cfg.Engine.Tun.Enabled,
		Stack:           cfg.Engine.Tun.Stack,
		Interface:       cfg.Engine.Tun.InterfaceName,
		MTU:             cfg.Engine.Tun.MTU,
		AutoRoute:       cfg.Engine.Tun.AutoRouteEnabled(),
		StrictRoute:     cfg.Engine.Tun.StrictRoute,
		PrivilegesOK:    priv.OK,
		PrivilegeDetail: priv.Detail,
	}

	st := StatusData{
		Vender:        c.version,
		PID:           os.Getpid(),
		RunDir:        c.runDir,
		Listen:        cfg.Proxy.Listen,
		Mode:          string(cfg.Proxy.Mode),
		Selected:      cfg.SelectedServer,
		EngineRunning: running,
		EnginePid:     pid,
		EngineVersion: engVersion,
		Restarting:    restarting,
		LastError:     lastErr,
		ClashListen:   c.clashAddr,
		Tun:           tun,
	}
	if servers != nil {
		st.ServerCount = servers.Len()
	}
	// Profile state: which profile is active, how many servers auto failover
	// would use, and every known profile name (so a client can cycle them).
	ps := profiles
	if ps == nil {
		ps = config.NewProfileSet()
	}
	st.Profile = ps.ActiveName()
	st.Profiles = config.KnownProfiles(servers, ps)
	if servers != nil {
		st.ProfileServers = servers.CountInProfile(ps.ActiveName())
	}
	if !startedAt.IsZero() && running {
		st.StartedAt = startedAt.Format(time.RFC3339)
		st.UptimeSecs = int(time.Since(startedAt).Seconds())
	}
	if running {
		// A dial to the mixed inbound: the same "is it usable" question
		// `vender status` answers, but answered in-process.
		st.EngineHealthy = c.engine.Healthy()
	}
	if servers != nil && cfg.SelectedServer != "" {
		if cfg.IsAutoServer() {
			st.SrvType = "auto"
			st.SrvEndpoint = fmt.Sprintf("%d servers", st.ProfileServers)
			st.SrvDetail = "failover (urltest over the active profile)"
		} else if srv, ok := servers.Get(cfg.SelectedServer); ok {
			st.SrvType = string(srv.Type)
			st.SrvEndpoint = srv.Endpoint()
			st.SrvDetail = srv.Detail()
		}
	}
	return st
}

// Ping implements Handler.
func (c *Controller) Ping() PingData {
	st := c.Status()
	return PingData{Vender: c.version, Proto: Version, PID: st.PID, RunDir: st.RunDir}
}

// Servers returns the configured servers, read from disk so a server imported
// from another terminal shows up without a restart.
func (c *Controller) Servers() ServersData {
	ss, err := c.store.LoadServers()
	c.mu.RLock()
	selected := c.cfg.SelectedServer
	c.mu.RUnlock()
	out := ServersData{Selected: selected}
	if err != nil {
		return out
	}
	c.mu.Lock()
	c.servers = ss
	c.mu.Unlock()
	for i := range ss.Servers {
		srv := &ss.Servers[i]
		out.Servers = append(out.Servers, ServerInfo{
			Name:     srv.Name,
			Type:     string(srv.Type),
			Endpoint: srv.Endpoint(),
			Detail:   srv.Detail(),
			Active:   strings.EqualFold(srv.Name, selected),
			Profile:  srv.ProfileName(),
		})
	}
	return out
}

// Latency measures direct ICMP independently of the current engine/outbound.
func (c *Controller) Latency(ctx context.Context, name string) LatencyData {
	out := LatencyData{Name: name, Method: "icmp-direct"}
	ss, err := c.store.LoadServers()
	if err != nil {
		out.Err = err.Error()
		return out
	}
	c.mu.RLock()
	selected, iface := c.cfg.SelectedServer, c.cfg.Engine.BindInterface
	c.mu.RUnlock()
	if name == "" {
		name = selected
	}
	if name == "" || strings.EqualFold(name, config.SelectedServerAuto) {
		out.Err = "auto failover: name the server to Ping"
		return out
	}
	srv, ok := ss.Get(name)
	if !ok {
		out.Err = fmt.Sprintf("no server named %q", name)
		return out
	}
	out.Name = srv.Name
	profiles, err := c.store.LoadProfiles()
	if err != nil {
		out.Err = err.Error()
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, core.DirectPingTimeout)
	defer cancel()
	var dns *config.ProfileDNS
	// DNSMap is renderer-specific and omits captured default nameservers. Ping
	// also accepts ordered profile defaults, so read the full own policy.
	if profile, ok := profiles.Get(srv.ProfileName()); ok {
		dns = profile.DNS
	}
	result, err := c.probe(ctx, core.PingRequest{Host: srv.Address, Interface: iface, DNS: dns})
	out.Ms, out.Sent, out.Received, out.PacketLoss = result.Ms, result.Sent, result.Received, result.PacketLoss
	out.Address, out.Interface = result.Address, result.Interface
	if err != nil {
		out.Err = err.Error()
	}
	return out
}

// Rules summarises the routing policy the engine is running with.
func (c *Controller) Rules() RulesData {
	c.mu.RLock()
	mode := c.cfg.Proxy.Mode
	c.mu.RUnlock()

	user, _ := rules.LoadUserRules(c.store.RulesPath())

	out := RulesData{
		Active: mode == config.ModeRule,
		Final:  string(rules.OutProxy),
		Source: fmt.Sprintf("%s @ %s", rules.RuleSetSourceRepo, rules.RuleSetSourceCommit),
	}
	if mode != config.ModeRule {
		out.Final = "direct"
		if mode == config.ModeGlobal {
			out.Final = string(rules.OutProxy)
		}
		dns := rules.GlobalDNS()
		if mode == config.ModeDirect {
			out.DNS.Detail = "direct mode has no DNS policy (the system resolver is used)"
		} else {
			out.DNS = describeDNS(dns)
		}
		return out
	}

	for _, a := range rules.BuiltinActions() {
		out.Actions = append(out.Actions, a.Summary())
	}
	policy, err := rules.EffectiveRules(user)
	if err != nil {
		out.DNS.Detail = err.Error()
		return out
	}
	for _, r := range policy {
		out.Rules = append(out.Rules, RuleInfo{Name: r.Name, Matches: r.Summary(), Outbound: string(r.Outbound)})
	}
	out.DNS = describeDNS(rules.RuleModeDNS(user))
	out.RuleSets = rules.ReferencedRuleSets()
	return out
}

// Proxies reports the engine's failover state: which member is active and how
// each server measures. It answers even when no failover group exists, so the
// TUI can use one code path for both single-server and auto mode.
func (c *Controller) Proxies(ctx context.Context) ProxiesData {
	ss, err := c.store.LoadServers()
	if err != nil {
		return ProxiesData{Nodes: []NodeHealth{}, Err: err.Error()}
	}
	c.mu.RLock()
	auto := c.cfg.IsAutoServer()
	mode := c.cfg.Proxy.Mode
	running := c.running
	c.mu.RUnlock()

	out := ProxiesData{Auto: auto, Nodes: make([]NodeHealth, 0, ss.Len())}
	if auto {
		// The policy always tags the failover group "proxy".
		out.Group = "proxy"
	}

	// Ask the engine first: it is the only component that knows which member
	// won the last URL test and how each member answered.
	live := map[string]core.ClashProxy{}
	active := ""
	if running && mode != config.ModeDirect && c.clash.Enabled() {
		if proxies, perr := c.clash.Proxies(ctx); perr == nil {
			live = proxies
			if g, ok := proxies["proxy"]; ok {
				active = g.Now
				if auto {
					out.Current = g.Now
				}
			}
		}
	} else if mode == config.ModeDirect {
		out.Err = "direct mode has no proxy outbound"
	} else if !running {
		out.Err = "the engine is not running"
	}

	for i := range ss.Servers {
		srv := &ss.Servers[i]
		n := NodeHealth{Name: srv.Name}
		// The engine names a server's outbound tag after the server, so the
		// live entry is keyed by the engine's tag (which is the server name, or
		// its prefixed form when the name collides with a reserved tag).
		if p, ok := live[core.ServerOutboundTag(srv)]; ok {
			if d := p.LastDelay(); d > 0 {
				n.DelayMs, n.Alive = d, true
			}
		}
		// In auto mode the group's Now is authoritative for "active"; in
		// single mode the selected server is active.
		if auto {
			n.Active = active != "" && strings.EqualFold(active, core.ServerOutboundTag(srv))
		} else {
			n.Active = strings.EqualFold(c.selectedServerName(), srv.Name)
		}
		out.Nodes = append(out.Nodes, n)
	}
	return out
}

// selectedServerName returns the configured selected_server, guarded.
func (c *Controller) selectedServerName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.SelectedServer
}

func describeDNS(d rules.DNS) DNSInfo {
	out := DNSInfo{Final: d.Final}
	for _, s := range d.Servers {
		where := "direct"
		if s.Detour != "" {
			where = "via " + s.Detour
		}
		addr := s.Server
		if s.Type == "fakeip" {
			addr = s.Inet4Range
		}
		out.Servers = append(out.Servers, fmt.Sprintf("%s = %s %s (%s)", s.Tag, s.Type, addr, where))
	}
	for _, r := range d.Rules {
		var match []string
		if len(r.RuleSets) > 0 {
			match = append(match, "rule_set=["+strings.Join(r.RuleSets, " ")+"]")
		}
		if len(r.Domains) > 0 {
			match = append(match, "domain=["+strings.Join(r.Domains, " ")+"]")
		}
		if len(r.DomainSuffixes) > 0 {
			match = append(match, "domain_suffix=["+strings.Join(r.DomainSuffixes, " ")+"]")
		}
		if len(r.QueryTypes) > 0 {
			match = append(match, "query_type=["+strings.Join(r.QueryTypes, " ")+"]")
		}
		out.Rules = append(out.Rules, fmt.Sprintf("%s (%s) -> %s", r.Name, strings.Join(match, " "), r.Server))
	}
	return out
}

// Call implements Handler: the request/response half of the protocol.
func (c *Controller) Call(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	switch cmd {
	case CmdPing:
		return c.Ping(), nil
	case CmdStatus:
		return c.Status(), nil
	case CmdServers:
		return c.Servers(), nil
	case CmdRules:
		return c.Rules(), nil
	case CmdProxies:
		return c.Proxies(ctx), nil
	case CmdSelect:
		var a SelectArgs
		if err := decodeArgs(args, &a); err != nil {
			return nil, err
		}
		if err := c.SelectServer(a.Name); err != nil {
			return nil, err
		}
		return ApplyData{Action: CmdSelect, Status: c.Status()}, nil
	case CmdMode:
		var a ModeArgs
		if err := decodeArgs(args, &a); err != nil {
			return nil, err
		}
		if err := c.SetMode(a.Mode); err != nil {
			return nil, err
		}
		return ApplyData{Action: CmdMode, Status: c.Status()}, nil
	case CmdTun:
		var a TunArgs
		if err := decodeArgs(args, &a); err != nil {
			return nil, err
		}
		if err := c.SetTun(a.Enabled); err != nil {
			return nil, err
		}
		return ApplyData{Action: CmdTun, Status: c.Status()}, nil
	case CmdProfile:
		var a ProfileArgs
		if err := decodeArgs(args, &a); err != nil {
			return nil, err
		}
		if err := c.SetProfile(a.Name); err != nil {
			return nil, err
		}
		return ApplyData{Action: CmdProfile, Status: c.Status()}, nil
	case CmdLatency:
		var a LatencyArgs
		if err := decodeArgs(args, &a); err != nil {
			return nil, err
		}
		return c.Latency(ctx, a.Name), nil
	case CmdStart:
		if err := c.StartEngine(); err != nil {
			return nil, wrapEngineErr(err)
		}
		return ApplyData{Action: CmdStart, Status: c.Status()}, nil
	case CmdStop:
		if err := c.StopEngine(); err != nil {
			return nil, wrapEngineErr(err)
		}
		return ApplyData{Action: CmdStop, Status: c.Status()}, nil
	case CmdRestart:
		if err := c.RestartEngine(); err != nil {
			return nil, wrapEngineErr(err)
		}
		return ApplyData{Action: CmdRestart, Status: c.Status()}, nil
	default:
		return nil, Errorf(KindBadRequest, "control: unknown command %q", cmd)
	}
}

// wrapEngineErr classifies an engine failure: a configuration problem
// (bad/missing server) is the user's to fix, anything else is internal.
func wrapEngineErr(err error) error {
	var pe *Error
	if errors.As(err, &pe) {
		return err
	}
	return Errorf(KindInternal, "control: %v", err)
}

func decodeArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return Errorf(KindBadRequest, "control: this command needs arguments")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return Errorf(KindBadRequest, "control: bad arguments: %v", err)
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

// allocateLoopbackAddr reserves a free loopback port and returns "127.0.0.1:p".
//
// sing-box accepts port 0 and reports the real port in its log, but parsing a
// log line to discover an interface is a fragile way to build a channel; a
// short-lived probe listener is not. The window between closing the probe and
// sing-box binding it is a few milliseconds on loopback, and the worst case is
// a clear "address already in use" from the engine at startup.
func allocateLoopbackAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("control: allocate a clash api port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("control: generate the clash api secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func enginePID(e core.Engine) int {
	if p, ok := e.(interface{ Pid() int }); ok {
		return p.Pid()
	}
	return 0
}

// onOff renders a boolean the way the daemon's log lines say it.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
