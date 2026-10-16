package core

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ll-lite/vender/internal/rules"
)

// Defaults for the supervision knobs. They are fields on engine (not package
// constants) so tests can shrink them.
const (
	defaultReadyTimeout = 10 * time.Second
	defaultPollInterval = 50 * time.Millisecond
	defaultStopGrace    = 5 * time.Second
	defaultDialTimeout  = 400 * time.Millisecond
	defaultLogBuffer    = 1024
	defaultTailLines    = 20
)

// engine supervises one sing-box child process at a time.
//
// Concurrency contract:
//
//   - mu guards every mutable field.
//   - Each Start installs a fresh `done` channel and closes it exactly once,
//     from the reaper goroutine, after the process has exited *and* both pipe
//     readers have drained. "The process has fully exited" is therefore
//     observable as a single channel receive, with no separate boolean to race
//     against.
//   - logs is likewise replaced per run and closed by the reaper right before
//     done, so ranging over Logs() terminates when the engine dies.
type engine struct {
	mu       sync.Mutex
	rc       RuntimeConfig
	logs     chan string
	logsDone bool
	tail     []string
	cmd      *exec.Cmd
	done     chan struct{}
	started  bool
	written  bool
	waitErr  error

	readyTimeout time.Duration
	pollInterval time.Duration
	stopGrace    time.Duration
	dialTimeout  time.Duration
}

// newEngine builds a stopped engine for rc. It does not touch the filesystem.
func newEngine(rc RuntimeConfig) *engine {
	return &engine{
		rc:           rc,
		logs:         make(chan string, defaultLogBuffer),
		readyTimeout: defaultReadyTimeout,
		pollInterval: defaultPollInterval,
		stopGrace:    defaultStopGrace,
		dialTimeout:  defaultDialTimeout,
	}
}

// WriteConfig renders rc into <RunDir>/singbox.json atomically and records it
// as the configuration the next Start will run.
//
// It also materialises the embedded rule sets under <RunDir>/rules whenever the
// rendered config declares any (rule mode, or a TUN route-exclusion set),
// because sing-box's route.rule_set only reads files. The files are written
// before the config that points at them, so a failed extraction never leaves a
// config referring to missing rule sets.
func (e *engine) WriteConfig(rc RuntimeConfig) error {
	if err := rc.Validate(); err != nil {
		return err
	}
	data, err := BuildConfig(rc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(rc.RunDir, 0o700); err != nil {
		return fmt.Errorf("core: create run dir %s: %w", rc.RunDir, err)
	}
	if len(declaredRuleSets(rc.Config)) > 0 {
		dir := filepath.Join(rc.RunDir, rules.RulesSubdir)
		if _, err := rules.WriteRuleSets(dir); err != nil {
			return fmt.Errorf("core: extract built-in rule sets: %w", err)
		}
	}
	// The generated config carries the server's credentials (uuid, password),
	// so it is written 0600 like servers.yaml.
	if err := writeFileAtomic(ConfigPath(rc.RunDir), data, 0o600); err != nil {
		return err
	}
	e.mu.Lock()
	e.rc = rc
	e.written = true
	e.mu.Unlock()
	return nil
}

// Start launches `sing-box run -c <RunDir>/singbox.json` and returns once the
// mixed inbound accepts TCP connections.
func (e *engine) Start() error {
	e.mu.Lock()
	if e.started {
		select {
		case <-e.done:
			// The previous child has fully exited; a restart is fine.
		default:
			e.mu.Unlock()
			return errors.New("core: engine is already running")
		}
	}
	if !e.written {
		e.mu.Unlock()
		return errors.New("core: engine configuration has not been written; call WriteConfig first")
	}
	rc := e.rc
	e.mu.Unlock()

	if rc.BinaryPath == "" {
		return errors.New("core: RuntimeConfig.BinaryPath is empty (resolve it with core.ResolveBinary)")
	}
	info, err := os.Stat(rc.BinaryPath)
	if err != nil {
		return fmt.Errorf("core: sing-box binary %s: %w", rc.BinaryPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("core: sing-box binary %s is a directory", rc.BinaryPath)
	}
	// Fail with a clear message when the port is already taken, instead of
	// letting sing-box fail to bind and then mistaking the *other* listener for
	// our own readiness probe.
	if err := e.ensureListenFree(rc.Config.Proxy.Listen); err != nil {
		return err
	}
	// TUN mode is the one thing sing-box cannot do without a kernel privilege,
	// and its own failure message ("open tun: TUNSETIFF: operation not
	// permitted") does not say what to do about it. Check first, and say.
	if rc.Config.Engine.Tun.Enabled {
		if err := CheckTUNPrivilege(rc.BinaryPath).Err(); err != nil {
			return err
		}
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("core: create stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return fmt.Errorf("core: create stderr pipe: %w", err)
	}

	cmd := exec.Command(rc.BinaryPath, "run", "-c", ConfigPath(rc.RunDir))
	cmd.Dir = rc.RunDir
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	prepareProcess(cmd)

	if err := cmd.Start(); err != nil {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return fmt.Errorf("core: start sing-box: %w", err)
	}
	// The parent must drop its copies of the write ends, or the readers never
	// observe EOF when the child dies.
	stdoutW.Close()
	stderrW.Close()

	e.mu.Lock()
	// Reuse the log channel when it is still open, so a caller that already
	// holds Logs() (M3's TUI, a daemon that subscribes before starting) sees
	// this run's output. After a stop the channel is closed and cannot be
	// reused, so a restart installs a fresh one.
	if e.logsDone {
		e.logs = make(chan string, defaultLogBuffer)
		e.logsDone = false
	}
	logs := e.logs
	done := make(chan struct{})
	e.cmd = cmd
	e.done = done
	e.started = true
	e.tail = nil
	e.waitErr = nil
	e.mu.Unlock()

	var readers sync.WaitGroup
	readers.Add(2)
	go e.pump(&readers, stdoutR, logs)
	go e.pump(&readers, stderrR, logs)
	go e.reap(cmd, &readers, logs, done)

	if err := e.waitReady(done); err != nil {
		// The listener never came up. Do not leave a half-started process
		// behind: Stop is idempotent and reaps it if it is still alive.
		_ = e.Stop()
		return err
	}
	return nil
}

// ensureListenFree fails early when something already accepts connections on
// addr.
//
// Start's readiness probe is a TCP dial, which cannot tell our sing-box apart
// from whatever was already listening: an occupied port would make a failed
// start look successful. Checking first turns that into an explicit error.
func (e *engine) ensureListenFree(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, e.dialTimeout)
	if err != nil {
		return nil
	}
	conn.Close()
	return fmt.Errorf("core: %s is already accepting connections; stop whatever is using it or change proxy.listen in config.yaml", addr)
}

// waitReady polls the mixed inbound until it accepts a TCP connection, the
// process dies, or the readiness timeout expires.
func (e *engine) waitReady(done <-chan struct{}) error {
	addr := e.rc.Config.Proxy.Listen
	deadline := time.Now().Add(e.readyTimeout)

	for {
		conn, err := net.DialTimeout("tcp", addr, e.dialTimeout)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-done:
			return fmt.Errorf("core: sing-box exited before it began listening on %s: %v%s",
				addr, e.waitError(), e.tailSuffix())
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("core: sing-box did not start listening on %s within %s%s",
				addr, e.readyTimeout, e.tailSuffix())
		}
		time.Sleep(e.pollInterval)
	}
}

// Stop terminates the engine gracefully and escalates to a kill after the
// grace period. It is idempotent and safe on a never-started engine.
func (e *engine) Stop() error {
	e.mu.Lock()
	cmd := e.cmd
	done := e.done
	started := e.started
	e.mu.Unlock()

	if !started || cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-done:
		return nil // already exited
	default:
	}

	if err := terminateProcess(cmd); err != nil && !isProcessGone(err) {
		// A failed graceful signal must not leave the child running.
		_ = killProcess(cmd)
	}
	if !waitClosed(done, e.stopGrace) {
		_ = killProcess(cmd)
		if !waitClosed(done, e.stopGrace) {
			return errors.New("core: sing-box did not exit after SIGKILL")
		}
	}
	return nil
}

// Healthy reports whether the process is alive and the mixed inbound accepts
// connections.
func (e *engine) Healthy() bool {
	e.mu.Lock()
	started, done, addr := e.started, e.done, e.rc.Config.Proxy.Listen
	e.mu.Unlock()

	if !started {
		return false
	}
	select {
	case <-done:
		return false
	default:
	}
	conn, err := net.DialTimeout("tcp", addr, e.dialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Logs streams engine output, line by line, until the process exits. The
// channel is closed once the process has exited and its output has been fully
// drained, so ranging over it terminates naturally.
//
// The channel returned before Start is the one the next run will use, but a
// restart installs a fresh channel; callers that want to follow every run
// should re-fetch it after each Start.
func (e *engine) Logs() <-chan string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.logs
}

// Pid returns the child's process id, or 0 when it is not running.
func (e *engine) Pid() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil || e.cmd.Process == nil {
		return 0
	}
	return e.cmd.Process.Pid
}

// --- internals -------------------------------------------------------------

// pump reads one pipe until EOF, forwarding lines to the shared log channel. A
// line is dropped rather than blocking when nobody is reading Logs(), so the
// child can never be wedged by a slow (or absent) consumer.
func (e *engine) pump(wg *sync.WaitGroup, r *os.File, logs chan<- string) {
	defer wg.Done()
	defer r.Close()
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if trimmed := strings.TrimRight(line, "\r\n"); trimmed != "" {
			e.record(trimmed)
			select {
			case logs <- trimmed:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

// reap waits for the process, joins the readers, and only then closes the log
// channel and reports "fully exited" on done. Closing the log channel after
// the readers guarantees every line the child emitted was delivered.
func (e *engine) reap(cmd *exec.Cmd, readers *sync.WaitGroup, logs chan string, done chan struct{}) {
	err := cmd.Wait()
	readers.Wait()

	e.mu.Lock()
	e.waitErr = err
	e.mu.Unlock()

	// logsDone is published before done is closed, so a Start that observes
	// "the previous run finished" also observes "the log channel is closed and
	// must be replaced".
	close(logs)
	e.mu.Lock()
	e.logsDone = true
	e.mu.Unlock()
	close(done)
}

func (e *engine) record(line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tail = append(e.tail, line)
	if len(e.tail) > defaultTailLines {
		e.tail = e.tail[len(e.tail)-defaultTailLines:]
	}
}

func (e *engine) waitError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waitErr
}

// tailSuffix renders the last few engine log lines for an error message, so a
// failed start explains itself in sing-box's own words rather than a bare
// timeout.
func (e *engine) tailSuffix() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.tail) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nlast output:\n")
	for _, line := range e.tail {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func waitClosed(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

func isProcessGone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, exec.ErrNotFound)
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so sing-box never reads a half-written config.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("core: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("core: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("core: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("core: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("core: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("core: replace %s: %w", path, err)
	}
	return nil
}
