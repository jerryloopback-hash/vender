package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Client is the client end of the control channel.
//
// A Client owns one connection, which carries request/response calls in
// sequence; concurrent calls are serialised, so a Client is safe to share. A
// subscription takes a connection over, so a client that wants to follow a
// stream *and* keep making calls uses two Clients (Dial twice); the TUI does
// exactly that.
type Client struct {
	// mu serialises request/response pairs on the connection. Two goroutines
	// sharing a Client (a periodic status poll and a one-off latency probe,
	// say) would otherwise interleave frames and desynchronise the decoder —
	// which is not a theoretical concern: it is a decoder panic waiting to
	// happen.
	mu     sync.Mutex
	conn   net.Conn
	enc    *json.Encoder
	dec    *json.Decoder
	token  string
	addr   string
	closed bool
	// stream is the name of the subscription that took this connection over,
	// if any. A subscribed client cannot also make calls: the connection is
	// dedicated to the stream, and mixing the two would silently interleave
	// frames. Callers that need both dial twice.
	stream string
}

// Dial connects to the daemon published in runDir's control.json.
func Dial(ctx context.Context, runDir string) (*Client, error) {
	ep, err := ReadEndpoint(runDir)
	if err != nil {
		return nil, err
	}
	return DialAddr(ctx, ep.Addr, ep.Token)
}

// DialAddr connects to an explicit endpoint. It is what tests and tools that
// already have the token use.
func DialAddr(ctx context.Context, addr, token string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("control: connect to %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	return &Client{
		conn:  conn,
		enc:   json.NewEncoder(conn),
		dec:   json.NewDecoder(bufio.NewReaderSize(conn, 64*1024)),
		token: token,
		addr:  addr,
	}, nil
}

// Addr returns the endpoint this client is connected to.
func (c *Client) Addr() string { return c.addr }

// Close releases the connection.
func (c *Client) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Call performs one request/response round trip and decodes the reply into out
// (which may be nil).
func (c *Client) Call(ctx context.Context, cmd string, args any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream != "" {
		return fmt.Errorf("control: this client follows the %q stream; dial a second client for calls", c.stream)
	}
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("control: encode args for %s: %w", cmd, err)
		}
		raw = b
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer c.conn.SetDeadline(time.Time{})
	}
	if err := c.enc.Encode(Request{V: Version, Auth: c.token, Cmd: cmd, Args: raw}); err != nil {
		return fmt.Errorf("control: send %s: %w", cmd, err)
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		return fmt.Errorf("control: read reply to %s: %w", cmd, err)
	}
	if resp.V != Version {
		return fmt.Errorf("control: daemon replied with protocol version %d (this client speaks %d)", resp.V, Version)
	}
	if !resp.OK {
		kind := resp.Kind
		if kind == "" {
			kind = KindInternal
		}
		return &Error{Kind: kind, Msg: resp.Error}
	}
	if out != nil && resp.Data != nil {
		// Round-trip through JSON so typed payloads decode the same way they
		// were encoded, without a second decoder at the call site.
		b, err := json.Marshal(resp.Data)
		if err != nil {
			return fmt.Errorf("control: re-encode reply to %s: %w", cmd, err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("control: decode reply to %s: %w", cmd, err)
		}
	}
	return nil
}

// Subscribe opens a stream on its own connection and returns it. The caller
// must Close the returned Stream.
func (c *Client) Subscribe(ctx context.Context, stream string) (*Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stream != "" {
		return nil, fmt.Errorf("control: this client already follows the %q stream; dial a second client", c.stream)
	}
	if err := c.enc.Encode(Request{V: Version, Auth: c.token, Cmd: stream}); err != nil {
		return nil, fmt.Errorf("control: send %s subscribe: %w", stream, err)
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("control: read %s acknowledgement: %w", stream, err)
	}
	if !resp.OK {
		kind := resp.Kind
		if kind == "" {
			kind = KindInternal
		}
		return nil, &Error{Kind: kind, Msg: resp.Error}
	}
	if resp.Stream != stream {
		return nil, fmt.Errorf("control: subscribed to %q but asked for %q", resp.Stream, stream)
	}
	_ = c.conn.SetDeadline(time.Time{}) // a stream is long-lived
	c.stream = stream
	return &Stream{name: stream, conn: c.conn, dec: c.dec, client: c}, nil
}

// Stream is a live subscription. Next blocks until the next event, the stream
// ends, or ctx is cancelled.
type Stream struct {
	name   string
	conn   net.Conn
	dec    *json.Decoder
	client *Client
	once   sync.Once
}

// Name returns the stream name.
func (s *Stream) Name() string { return s.name }

// Next returns the next non-heartbeat event payload, decoded into out. It
// returns io.EOF once the daemon closes the stream.
func (s *Stream) Next(ctx context.Context, out any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.conn.SetReadDeadline(deadline)
		defer s.conn.SetReadDeadline(time.Time{})
	}
	for {
		var ev Event
		if err := s.dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
				return io.EOF
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if ev.KeepAlive {
			continue
		}
		if out == nil {
			return nil
		}
		b, err := json.Marshal(ev.Data)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
}

// Close ends the subscription (and its connection).
func (s *Stream) Close() error {
	var err error
	s.once.Do(func() { err = s.client.Close() })
	return err
}

// --- typed convenience calls ------------------------------------------------

// Ping asks the daemon to identify itself. It is the cheapest liveness probe.
func (c *Client) Ping(ctx context.Context) (PingData, error) {
	var out PingData
	err := c.Call(ctx, CmdPing, nil, &out)
	return out, err
}

// Status fetches the daemon's current state.
func (c *Client) Status(ctx context.Context) (StatusData, error) {
	var out StatusData
	err := c.Call(ctx, CmdStatus, nil, &out)
	return out, err
}

// Servers lists the configured servers and marks the active one.
func (c *Client) Servers(ctx context.Context) (ServersData, error) {
	var out ServersData
	err := c.Call(ctx, CmdServers, nil, &out)
	return out, err
}

// Select switches the active server and restarts the engine.
func (c *Client) Select(ctx context.Context, name string) (ApplyData, error) {
	var out ApplyData
	err := c.Call(ctx, CmdSelect, SelectArgs{Name: name}, &out)
	return out, err
}

// SetMode changes the routing mode and restarts the engine.
func (c *Client) SetMode(ctx context.Context, mode string) (ApplyData, error) {
	var out ApplyData
	err := c.Call(ctx, CmdMode, ModeArgs{Mode: mode}, &out)
	return out, err
}

// Latency measures a round trip through the named server (empty: the active
// one). A failed probe is reported in LatencyData.Err, not as an error.
func (c *Client) Latency(ctx context.Context, name string) (LatencyData, error) {
	var out LatencyData
	err := c.Call(ctx, CmdLatency, LatencyArgs{Name: name}, &out)
	return out, err
}

// Rules summarises the active routing policy.
func (c *Client) Rules(ctx context.Context) (RulesData, error) {
	var out RulesData
	err := c.Call(ctx, CmdRules, nil, &out)
	return out, err
}

// StartEngine starts the engine inside the daemon.
func (c *Client) StartEngine(ctx context.Context) (ApplyData, error) {
	var out ApplyData
	err := c.Call(ctx, CmdStart, nil, &out)
	return out, err
}

// StopEngine stops the engine inside the daemon (the daemon itself stays up).
func (c *Client) StopEngine(ctx context.Context) (ApplyData, error) {
	var out ApplyData
	err := c.Call(ctx, CmdStop, nil, &out)
	return out, err
}

// RestartEngine stops and starts the engine in place.
func (c *Client) RestartEngine(ctx context.Context) (ApplyData, error) {
	var out ApplyData
	err := c.Call(ctx, CmdRestart, nil, &out)
	return out, err
}

// FollowLogs opens the log stream.
func (c *Client) FollowLogs(ctx context.Context) (*Stream, error) {
	return c.Subscribe(ctx, StreamLogs)
}

// WatchTraffic opens the traffic stream.
func (c *Client) WatchTraffic(ctx context.Context) (*Stream, error) {
	return c.Subscribe(ctx, StreamTraffic)
}

// IsNotRunning reports whether err means "no daemon is published in this run
// dir" (as opposed to a broken endpoint or a failed connection). The TUI uses
// it to choose between "start it with `vender daemon`" and showing the real
// error.
func IsNotRunning(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
