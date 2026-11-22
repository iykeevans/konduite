package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
)

const helloType = "runtime.hello"

var errBinary = errors.New("gateway: binary message")

// Conn is one authenticated runtime connection. The ids come from the
// verified access token, never from a frame.
type Conn struct {
	RuntimeID   uuid.UUID
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID

	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte
	pumps  sync.WaitGroup

	closeOnce sync.Once
	closing   chan struct{}
	code      websocket.StatusCode
	reason    string
}

func newConn(ctx context.Context, ws *websocket.Conn, claims runtimes.Claims) *Conn {
	ctx, cancel := context.WithCancel(ctx)
	return &Conn{
		RuntimeID:   claims.RuntimeID,
		WorkspaceID: claims.WorkspaceID,
		OwnerID:     claims.OwnerID,
		ws:          ws,
		ctx:         ctx,
		cancel:      cancel,
		out:         make(chan []byte, sendQueue),
		closing:     make(chan struct{}),
	}
}

// Send validates f against its contract and queues it for the runtime. It
// blocks while the queue is full, until ctx ends or the connection closes.
// A nil error means queued, not received: delivery is confirmed only by the
// runtime's runtime.event_ack.
func (c *Conn) Send(ctx context.Context, f contracts.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err := validate.Frame(b); err != nil {
		return fmt.Errorf("gateway: outgoing %s frame: %w", f.Type, err)
	}
	select {
	case c.out <- b:
		return nil
	case <-c.ctx.Done():
		return errClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close asks the connection to close with code and reason and returns at
// once. The first call wins.
func (c *Conn) Close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.code, c.reason = code, reason
		close(c.closing)
	})
}

type closeDecision struct {
	code   websocket.StatusCode
	reason string
	err    error
}

func (g *Gateway) serve(c *Conn) {
	frames := make(chan []byte)
	readErr := make(chan error, 1)
	c.pumps.Go(func() { c.readPump(frames, readErr) })

	d := g.session(c, frames, readErr)
	if d.code == 0 {
		_ = c.ws.CloseNow()
	} else {
		_ = c.ws.Close(d.code, d.reason)
	}
	c.cancel()
	c.pumps.Wait()

	attrs := []any{"runtime_id", c.RuntimeID, "workspace_id", c.WorkspaceID}
	if d.code != 0 {
		attrs = append(attrs, "close_code", int(d.code), "close_reason", d.reason)
	} else {
		attrs = append(attrs, "peer_close_code", int(websocket.CloseStatus(d.err)))
	}
	if d.err != nil && d.code != 0 {
		attrs = append(attrs, "error", d.err)
	}
	slog.InfoContext(c.ctx, "runtime disconnected", attrs...)
}

// session runs the connection until it should close and says how.
// A zero code means the peer is already gone and there is nothing to send.
func (g *Gateway) session(c *Conn, frames <-chan []byte, readErr <-chan error) closeDecision {
	timer := time.NewTimer(g.cfg.HelloTimeout)
	defer timer.Stop()
	var first []byte
	select {
	case first = <-frames:
	case err := <-readErr:
		return readFailed(err)
	case <-timer.C:
		return closeDecision{code: StatusHelloTimeout, reason: "runtime.hello not received in time"}
	case <-c.closing:
		return closeDecision{code: c.code, reason: c.reason}
	}

	if d, ok := g.hello(c, first); !ok {
		return d
	}
	g.register(c)
	unsubscribe, err := g.cfg.Subscriber.Subscribe(Subject(c.RuntimeID), func(m nats.Msg) {
		g.cfg.Deliverer.Notify(c.ctx, c, m)
	})
	if err != nil {
		return closeDecision{code: websocket.StatusInternalError, reason: "internal error", err: err}
	}
	defer func() {
		if err := unsubscribe(); err != nil {
			slog.WarnContext(c.ctx, "runtime unsubscribe failed", "runtime_id", c.RuntimeID, "error", err)
		}
	}()
	c.pumps.Go(func() { c.writePump(g.cfg.WriteTimeout) })
	c.pumps.Go(func() { c.pinger(g.cfg.PingInterval, g.cfg.PongTimeout) })
	slog.InfoContext(c.ctx, "runtime connected", "runtime_id", c.RuntimeID, "workspace_id", c.WorkspaceID)

	for {
		select {
		case raw := <-frames:
			f, err := validate.Frame(raw)
			if err != nil {
				return closeDecision{code: websocket.StatusPolicyViolation, reason: "invalid frame", err: err}
			}
			if f.Type == helloType {
				return closeDecision{code: websocket.StatusPolicyViolation, reason: "runtime.hello already received"}
			}
			if err := g.cfg.Receiver.Receive(c.ctx, c, f); err != nil {
				return closeDecision{code: websocket.StatusInternalError, reason: "internal error", err: err}
			}
		case err := <-readErr:
			return readFailed(err)
		case <-c.closing:
			return closeDecision{code: c.code, reason: c.reason}
		}
	}
}

func (g *Gateway) hello(c *Conn, raw []byte) (closeDecision, bool) {
	f, err := validate.Frame(raw)
	if err != nil || f.Type != helloType {
		return closeDecision{code: websocket.StatusPolicyViolation, reason: "first frame must be a valid runtime.hello", err: err}, false
	}
	var hello contracts.RuntimeHello
	if err := json.Unmarshal(f.Data, &hello); err != nil {
		return closeDecision{code: websocket.StatusPolicyViolation, reason: "first frame must be a valid runtime.hello", err: err}, false
	}
	_, err = g.cfg.Agents.SyncFromHello(c.ctx, agents.Runtime{ID: c.RuntimeID, WorkspaceID: c.WorkspaceID}, hello)
	if e, ok := errs.As(err); ok {
		switch e.Kind {
		case errs.KindNotFound:
			return closeDecision{code: StatusRuntimeNotFound, reason: "runtime not found"}, false
		case errs.KindInvalid:
			return closeDecision{code: websocket.StatusPolicyViolation, reason: e.Message, err: err}, false
		}
	}
	if err != nil {
		return closeDecision{code: websocket.StatusInternalError, reason: "internal error", err: err}, false
	}
	return closeDecision{}, true
}

func readFailed(err error) closeDecision {
	if errors.Is(err, errBinary) {
		return closeDecision{code: websocket.StatusPolicyViolation, reason: "frames must be text", err: err}
	}
	return closeDecision{err: err}
}

func (c *Conn) readPump(frames chan<- []byte, readErr chan<- error) {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err == nil && typ != websocket.MessageText {
			err = errBinary
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

func (c *Conn) writePump(timeout time.Duration) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			ctx, cancel := context.WithTimeout(c.ctx, timeout)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				_ = c.ws.CloseNow()
				return
			}
		}
	}
}

// pinger drops the connection when a pong does not come back in time, so a
// peer that vanished without closing is noticed. RT-005's heartbeat frames
// decide online status; this only frees the socket.
func (c *Conn) pinger(interval, timeout time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, timeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				_ = c.ws.CloseNow()
				return
			}
		}
	}
}
