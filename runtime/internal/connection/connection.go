// Package connection holds the runtime's WebSocket to the API (ADR-007): it
// dials /v1/runtime/connect with a runtime access token in the Authorization
// header, sends runtime.hello, and runs the read and write pumps with
// ping/pong until the context ends or the connection drops. Every frame in
// either direction is checked against the contracts.
//
// One Dial is one connection. Reconnecting with backoff and re-authenticating
// before the token expires is RT-006, built on top of Dial and Run; the
// 15-second heartbeat frames are RT-005, sent through Send.
package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/coder/websocket"

	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
)

// Path is the server's connect endpoint.
const Path = "/v1/runtime/connect"

// Close codes the server sends besides the standard ones (1001 going away,
// 1008 invalid frame, 1011 server error). They mirror backend/internal/gateway.
const (
	StatusHelloTimeout    websocket.StatusCode = 4001
	StatusReplaced        websocket.StatusCode = 4002
	StatusRuntimeNotFound websocket.StatusCode = 4003
)

const (
	defaultPingInterval = 30 * time.Second
	defaultPongTimeout  = 10 * time.Second
	defaultWriteTimeout = 10 * time.Second
	maxFrameBytes       = 1 << 20
	sendQueue           = 64
)

// ErrUnauthorized means the server refused the access token before
// upgrading: it expired, was forged, or its runtime is revoked.
var ErrUnauthorized = errors.New("server rejected the runtime access token")

// ErrClosed is returned by Send once the connection is closed.
var ErrClosed = errors.New("connection closed")

// Handler receives each frame the server sends, already validated. An error
// closes the connection with 1011 and is returned by Run.
type Handler func(ctx context.Context, f contracts.Frame) error

// Options configure Dial. ServerURL, Token and Hello are required.
type Options struct {
	// ServerURL is the API's http(s) base URL, as in the config file.
	ServerURL string
	// Token is a runtime access token from /v1/runtime/token.
	Token string
	Hello contracts.RuntimeHello
	// HTTPClient makes the upgrade request; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// Handler gets the server's frames; nil drops them.
	Handler Handler

	PingInterval time.Duration // default 30s
	PongTimeout  time.Duration // default 10s
	WriteTimeout time.Duration // default 10s
}

// Disconnected is returned by Run when the connection ends without the
// caller asking. Code is the close code the server sent, or -1 when the
// connection dropped without one.
type Disconnected struct {
	Code   websocket.StatusCode
	Reason string
	Err    error
}

func (d *Disconnected) Error() string {
	if d.Code == -1 {
		return fmt.Sprintf("connection lost: %v", d.Err)
	}
	if d.Reason != "" {
		return fmt.Sprintf("server closed the connection: %s (%d)", d.Reason, int(d.Code))
	}
	return fmt.Sprintf("server closed the connection (%d)", int(d.Code))
}

func (d *Disconnected) Unwrap() error { return d.Err }

// Conn is an open connection that has sent runtime.hello.
type Conn struct {
	ws   *websocket.Conn
	opts Options
	out  chan []byte

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// Dial connects, authenticates and sends runtime.hello. Call Run next.
func Dial(ctx context.Context, opts Options) (*Conn, error) {
	if opts.Token == "" {
		return nil, errors.New("connection: no access token")
	}
	target, err := connectURL(opts.ServerURL)
	if err != nil {
		return nil, err
	}
	hello, err := NewFrame("runtime.hello", opts.Hello)
	if err != nil {
		return nil, err
	}
	raw, err := encode(hello)
	if err != nil {
		return nil, err
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.PingInterval, defaultPingInterval)
	def(&opts.PongTimeout, defaultPongTimeout)
	def(&opts.WriteTimeout, defaultWriteTimeout)

	ws, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{
		HTTPClient: opts.HTTPClient,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + opts.Token}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("connect to %s: %w", target, err)
	}
	ws.SetReadLimit(maxFrameBytes)

	wctx, cancel := context.WithTimeout(ctx, opts.WriteTimeout)
	defer cancel()
	if err := ws.Write(wctx, websocket.MessageText, raw); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("send runtime.hello: %w", err)
	}
	cctx, ccancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Conn{ws: ws, opts: opts, out: make(chan []byte, sendQueue), ctx: cctx, cancel: ccancel}, nil
}

// Run pumps frames until ctx ends, which closes the connection normally and
// returns nil, or until the connection ends some other way, which returns
// a *Disconnected or the Handler's error. Run must be called once.
func (c *Conn) Run(ctx context.Context) error {
	frames := make(chan []byte)
	readErr := make(chan error, 1)
	var pumps sync.WaitGroup
	pumps.Go(func() { c.readPump(frames, readErr) })
	pumps.Go(c.writePump)
	pumps.Go(c.pinger)
	defer func() {
		c.cancel()
		pumps.Wait()
	}()

	for {
		select {
		case <-ctx.Done():
			c.close(websocket.StatusNormalClosure, "runtime stopping")
			return nil
		case err := <-readErr:
			_ = c.ws.CloseNow()
			return disconnected(err)
		case raw := <-frames:
			f, err := validate.Frame(raw)
			if err != nil {
				c.close(websocket.StatusPolicyViolation, "invalid frame")
				return fmt.Errorf("server sent an invalid frame: %w", err)
			}
			if c.opts.Handler == nil {
				continue
			}
			if err := c.opts.Handler(ctx, f); err != nil {
				c.close(websocket.StatusInternalError, "runtime error")
				return err
			}
		}
	}
}

// Send validates f and queues it for the server. It blocks while the queue
// is full, until ctx ends or the connection closes.
func (c *Conn) Send(ctx context.Context, f contracts.Frame) error {
	raw, err := encode(f)
	if err != nil {
		return err
	}
	select {
	case c.out <- raw:
		return nil
	case <-c.ctx.Done():
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewFrame wraps a payload in a version 1 frame with a fresh id.
func NewFrame(typ string, data any) (contracts.Frame, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return contracts.Frame{}, err
	}
	return contracts.Frame{ID: uuid.New().String(), Type: typ, Version: 1, SentAt: time.Now().UTC(), Data: raw}, nil
}

func encode(f contracts.Frame) ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if _, err := validate.Frame(raw); err != nil {
		return nil, fmt.Errorf("outgoing %s frame: %w", f.Type, err)
	}
	return raw, nil
}

func (c *Conn) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() { _ = c.ws.Close(code, reason) })
}

func disconnected(err error) error {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return &Disconnected{Code: ce.Code, Reason: ce.Reason, Err: err}
	}
	return &Disconnected{Code: -1, Err: err}
}

func (c *Conn) readPump(frames chan<- []byte, readErr chan<- error) {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err == nil && typ != websocket.MessageText {
			err = errors.New("server sent a binary message")
		}
		if err != nil {
			readErr <- err
			return
		}
		select {
		case frames <- data:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Conn) writePump() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case raw := <-c.out:
			ctx, cancel := context.WithTimeout(c.ctx, c.opts.WriteTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, raw)
			cancel()
			if err != nil {
				_ = c.ws.CloseNow()
				return
			}
		}
	}
}

func (c *Conn) pinger() {
	t := time.NewTicker(c.opts.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, c.opts.PongTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				_ = c.ws.CloseNow()
				return
			}
		}
	}
}

func connectURL(server string) (string, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("connection: invalid server URL %q", server)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("connection: server URL must be http or https, got %q", u.Scheme)
	}
	u.Path += Path
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
