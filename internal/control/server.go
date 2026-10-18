package control

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Handler is what the server needs from the daemon behind it. Controller
// implements it; tests supply fakes.
type Handler interface {
	// Call handles one request/response command. The returned value is
	// marshalled into Response.Data.
	Call(ctx context.Context, cmd string, args json.RawMessage) (any, error)
	// Subscribe registers a stream and returns its channel plus a cancel
	// function. Values sent on the channel are marshalled into Event.Data.
	// Cancel is idempotent, and unregisters the subscriber.
	Subscribe(name string) (<-chan any, func(), error)
	// Status is the cheap snapshot used by Ping.
	Status() StatusData
}

// Server is the daemon side of the control channel: a loopback listener plus
// token authentication.
type Server struct {
	ln      net.Listener
	token   string
	handler Handler

	mu      sync.Mutex
	closed  bool
	conns   map[net.Conn]struct{}
	done    chan struct{}
	wg      sync.WaitGroup
	version string // the vender version reported by Ping
	runDir  string
}

// ServerOptions configures NewServer.
type ServerOptions struct {
	Handler Handler
	// Version is the vender build string reported through the channel.
	Version string
	// RunDir is where the endpoint file is published. When empty, no endpoint
	// file is written (the server is then only reachable via Addr), which is
	// what unit tests want.
	RunDir string
	// Addr overrides the listen address. Empty means loopback with an
	// OS-assigned port, which is what production uses.
	Addr string
	// Token overrides the generated token (tests only).
	Token string
}

// NewServer starts listening and publishes its endpoint. Call Serve to accept
// connections and Close to shut down.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Handler == nil {
		return nil, errors.New("control: ServerOptions.Handler is required")
	}
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("control: listen on %s: %w", addr, err)
	}
	token := opts.Token
	if token == "" {
		token, err = newToken()
		if err != nil {
			ln.Close()
			return nil, err
		}
	}
	s := &Server{
		ln:      ln,
		token:   token,
		handler: opts.Handler,
		conns:   make(map[net.Conn]struct{}),
		done:    make(chan struct{}),
		version: opts.Version,
		runDir:  opts.RunDir,
	}
	if opts.RunDir != "" {
		ep := Endpoint{
			V:       Version,
			PID:     os.Getpid(),
			Addr:    ln.Addr().String(),
			Token:   token,
			Started: time.Now().Format(time.RFC3339),
			RunDir:  opts.RunDir,
			Vender:  opts.Version,
		}
		if err := WriteEndpoint(opts.RunDir, ep); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return s, nil
}

// Addr returns the address the server listens on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Token returns the token clients must present.
func (s *Server) Token() string { return s.token }

// Endpoint returns the published endpoint.
func (s *Server) Endpoint() Endpoint {
	return Endpoint{V: Version, PID: os.Getpid(), Addr: s.Addr(), Token: s.token, RunDir: s.runDir}
}

// Serve accepts connections until Close. Each connection gets its own
// goroutine, so a request that takes a second (a server switch restarts the
// engine) never blocks `status` on another connection.
func (s *Server) Serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				conn.Close()
			}()
			s.serveConn(conn)
		}()
	}
}

// Close stops accepting, drops every live connection (this is what unblocks a
// subscriber still reading a stream) and waits for the handlers to return.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	close(s.done)
	err := s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	if s.runDir != "" {
		RemoveEndpoint(s.runDir)
	}
	return err
}

// serveConn handles one connection: newline-delimited JSON requests until the
// peer goes away. A subscription takes the connection over.
func (s *Server) serveConn(conn net.Conn) {
	br := bufio.NewReaderSize(conn, 64*1024)
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(br)
	// One reader owns the decoder. Detect peer EOF while a Ping is running so
	// closing the dedicated client cancels the socket/DNS work in the daemon.
	connCtx, stopConn := context.WithCancel(context.Background())
	defer stopConn()
	defer conn.Close()
	type incoming struct {
		req Request
		err error
	}
	frames := make(chan incoming, 1)
	readerDone := make(chan struct{})
	defer func() { stopConn(); conn.Close(); <-readerDone }()
	go func() {
		defer close(readerDone)
		defer close(frames)
		defer stopConn()
		for {
			var frame incoming
			frame.err = dec.Decode(&frame.req)
			if frame.err != nil {
				stopConn()
				select {
				case frames <- frame:
				default:
				}
				return
			}
			select {
			case frames <- frame:
			case <-connCtx.Done():
				return
			}
		}
	}()
	for frame := range frames {
		req := frame.req
		if err := frame.err; err != nil {
			if !errors.Is(err, io.EOF) && !isClosedConn(err) {
				// A malformed frame is worth reporting; the client is
				// probably hand-rolled or a different version.
				_ = enc.Encode(Response{V: Version, OK: false, Kind: KindBadRequest,
					Error: fmt.Sprintf("control: malformed request: %v", err)})
			}
			return
		}

		if err := s.authorize(req); err != nil {
			_ = enc.Encode(Response{V: Version, ID: req.ID, OK: false, Kind: KindOf(err), Error: err.Error()})
			return // do not keep serving an unauthenticated peer
		}

		if stream, ok := AsStream(req.Cmd); ok {
			data, cancel, err := s.handler.Subscribe(stream)
			if err != nil {
				_ = enc.Encode(Response{V: Version, ID: req.ID, OK: false, Kind: KindOf(err), Error: err.Error()})
				return
			}
			_ = enc.Encode(Response{V: Version, ID: req.ID, OK: true, Stream: stream})
			s.pumpStream(connCtx, conn, enc, stream, data)
			cancel()
			return
		}

		ctx, cancel := context.WithTimeout(connCtx, callTimeout(req.Cmd))
		data, err := s.handler.Call(ctx, req.Cmd, req.Args)
		cancel()
		resp := Response{V: Version, ID: req.ID, OK: err == nil}
		if err != nil {
			resp.Kind = KindOf(err)
			resp.Error = err.Error()
		} else {
			resp.Data = data
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// pumpStream forwards stream values as event frames until the peer
// disconnects or the stream ends.
func (s *Server) pumpStream(ctx context.Context, conn net.Conn, enc *json.Encoder, stream string, data <-chan any) {
	// A write deadline is refreshed per event: a client that stops reading
	// (a suspended laptop) must not pin the daemon's goroutine forever.
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case v, ok := <-data:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := enc.Encode(Event{V: Version, Stream: stream, Data: v}); err != nil {
				return
			}
		case <-ping.C:
			// A keepalive keeps NAT/space heaters from killing an idle stream
			// and gives the write path a chance to notice a dead peer even
			// while nothing is happening on the engine.
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := enc.Encode(Event{V: Version, Stream: stream, KeepAlive: true}); err != nil {
				return
			}
		case <-ctx.Done():
			return
		case <-s.done:
			return
		}
	}
}

// AsStream maps a command name to a stream name.
func AsStream(cmd string) (string, bool) {
	switch cmd {
	case StreamLogs, StreamTraffic:
		return cmd, true
	default:
		return "", false
	}
}

// authorize checks the protocol version and the token. The comparison is
// constant time so a client cannot probe the token byte by byte.
func (s *Server) authorize(req Request) error {
	if req.V != Version {
		return Errorf(KindAuth, "control: protocol version %d, this daemon speaks %d", req.V, Version)
	}
	if subtle.ConstantTimeCompare([]byte(req.Auth), []byte(s.token)) != 1 {
		return Errorf(KindAuth, "control: bad or missing token")
	}
	return nil
}

// callTimeout bounds a request. A server switch stops and restarts the engine
// inside a single call, so this is generous; it exists to make a wedged engine
// surface as an error instead of a hung TUI.
func callTimeout(cmd string) time.Duration {
	switch cmd {
	case CmdSelect, CmdMode, CmdRestart, CmdStart, CmdStop:
		return 60 * time.Second
	default:
		return 15 * time.Second
	}
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("control: generate token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func isClosedConn(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so a reader never observes a partial endpoint file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("control: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("control: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("control: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("control: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("control: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("control: replace %s: %w", path, err)
	}
	return nil
}
