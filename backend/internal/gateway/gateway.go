// Package gateway holds runtime WebSocket connections (ADR-007): it
// authenticates the upgrade with a runtime access token, requires
// runtime.hello, validates every frame against the contracts, keeps one
// connection per runtime and, while a runtime is connected, subscribes to its
// NATS subject so whatever is published for it reaches this API instance.
//
// What happens to those messages and to the runtime's frames after hello is
// injected: Deliverer (MSG-008 pushes deliveries) and Receiver (RT-005
// heartbeats, MSG-008 acks).
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
	"github.com/iykeevans/konduite/packages/contracts"
)

// Path is where runtimes connect.
const Path = "/v1/runtime/connect"

// Close codes the gateway sends besides the standard ones: 1008 (policy
// violation) for an invalid or unexpected frame, 1001 (going away) on
// shutdown and 1011 (internal error) when the server fails. The runtime
// decides whether to reconnect from these; docs/protocols/runtime-websocket.md
// lists them.
const (
	// StatusHelloTimeout: no runtime.hello within the hello timeout.
	StatusHelloTimeout websocket.StatusCode = 4001
	// StatusReplaced: a newer connection for the same runtime took over.
	// The runtime must not reconnect, or two processes would keep taking
	// the socket from each other.
	StatusReplaced websocket.StatusCode = 4002
	// StatusRuntimeNotFound: the token's runtime no longer exists.
	StatusRuntimeNotFound websocket.StatusCode = 4003
)

const (
	defaultHelloTimeout = 5 * time.Second
	defaultPingInterval = 30 * time.Second
	defaultPongTimeout  = 10 * time.Second
	defaultWriteTimeout = 10 * time.Second
	maxFrameBytes       = 64 << 10
	sendQueue           = 64
)

// TokenVerifier checks a runtime access token and returns its claims.
type TokenVerifier func(token string) (runtimes.Claims, error)

// AgentSyncer records the agents a runtime announces. agents.Service is the
// production implementation.
type AgentSyncer interface {
	SyncFromHello(ctx context.Context, rt agents.Runtime, hello contracts.RuntimeHello) ([]agents.Agent, error)
}

// Subscriber subscribes to a NATS subject. nats.Client is the production
// implementation.
type Subscriber interface {
	Subscribe(subject string, fn func(nats.Msg)) (unsubscribe func() error, err error)
}

// Deliverer receives what is published on a connected runtime's subject.
// Notify runs on the subscription's goroutine, one message at a time, and
// must return quickly; ctx ends when the connection does.
type Deliverer interface {
	Notify(ctx context.Context, c *Conn, m nats.Msg)
}

// Receiver gets each frame a runtime sends after runtime.hello, already
// validated against its contract. An error closes the connection with 1011.
type Receiver interface {
	Receive(ctx context.Context, c *Conn, f contracts.Frame) error
}

// Config wires a Gateway. Verify, Agents and Subscriber are required; nil
// Deliverer and Receiver get Unwired. Zero durations get the defaults.
type Config struct {
	Verify     TokenVerifier
	Agents     AgentSyncer
	Subscriber Subscriber
	Deliverer  Deliverer
	Receiver   Receiver

	HelloTimeout time.Duration // default 5s (ADR-007)
	PingInterval time.Duration // default 30s
	PongTimeout  time.Duration // default 10s
	WriteTimeout time.Duration // default 10s
}

// Gateway serves Path. It is safe for concurrent use.
type Gateway struct {
	cfg Config

	mu       sync.Mutex
	closing  bool
	sessions map[*Conn]struct{}
	byID     map[uuid.UUID]*Conn
	wg       sync.WaitGroup
}

// New returns a Gateway.
func New(cfg Config) *Gateway {
	if cfg.Verify == nil || cfg.Agents == nil || cfg.Subscriber == nil {
		panic("gateway: Verify, Agents and Subscriber are required")
	}
	if cfg.Deliverer == nil {
		cfg.Deliverer = Unwired{}
	}
	if cfg.Receiver == nil {
		cfg.Receiver = Unwired{}
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.HelloTimeout, defaultHelloTimeout)
	def(&cfg.PingInterval, defaultPingInterval)
	def(&cfg.PongTimeout, defaultPongTimeout)
	def(&cfg.WriteTimeout, defaultWriteTimeout)
	return &Gateway{cfg: cfg, sessions: map[*Conn]struct{}{}, byID: map[uuid.UUID]*Conn{}}
}

// Routes mounts Path. It authenticates the runtime itself, so it belongs in
// api.Deps.PublicRoutes, outside the Supabase middleware.
func (g *Gateway) Routes(r chi.Router) {
	r.Get(Path, g.connect)
}

// Subject is the NATS subject filter a connected runtime is subscribed to.
func Subject(runtimeID uuid.UUID) string {
	return "agenthub.rt." + runtimeID.String() + ".>"
}

// Shutdown stops accepting connections, closes every open one with 1001 and
// waits for them to finish. When ctx ends first, the rest are dropped
// without a close handshake and ctx's error is returned.
//
// http.Server.Shutdown does not wait for hijacked connections, so the API
// process calls this after the HTTP server has stopped.
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.mu.Lock()
	g.closing = true
	open := make([]*Conn, 0, len(g.sessions))
	for c := range g.sessions {
		open = append(open, c)
	}
	g.mu.Unlock()

	for _, c := range open {
		c.Close(websocket.StatusGoingAway, "server shutting down")
	}
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		for _, c := range open {
			_ = c.ws.CloseNow()
		}
		return ctx.Err()
	}
}

func (g *Gateway) connect(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		httpx.Error(w, r, errs.Unauthenticated("missing runtime access token"))
		return
	}
	claims, err := g.cfg.Verify(token)
	if err != nil {
		httpx.Error(w, r, errs.Unauthenticated("invalid runtime access token"))
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.InfoContext(r.Context(), "runtime websocket upgrade failed",
			"runtime_id", claims.RuntimeID, "error", err)
		return
	}
	ws.SetReadLimit(maxFrameBytes)
	c := newConn(context.WithoutCancel(r.Context()), ws, claims)
	if !g.track(c) {
		_ = ws.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer g.untrack(c)
	g.serve(c)
}

func (g *Gateway) track(c *Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.sessions[c] = struct{}{}
	g.wg.Add(1)
	return true
}

func (g *Gateway) untrack(c *Conn) {
	g.mu.Lock()
	delete(g.sessions, c)
	if g.byID[c.RuntimeID] == c {
		delete(g.byID, c.RuntimeID)
	}
	g.mu.Unlock()
	g.wg.Done()
}

// register makes c the runtime's connection and closes the one it replaces.
// The newer connection wins: the older one is the likelier to be dead (a
// laptop that slept, a socket whose peer vanished), and the newer one has
// just proved it holds a valid token.
func (g *Gateway) register(c *Conn) {
	g.mu.Lock()
	old := g.byID[c.RuntimeID]
	g.byID[c.RuntimeID] = c
	g.mu.Unlock()
	if old != nil {
		old.Close(StatusReplaced, "replaced by a newer connection for this runtime")
	}
}

// Unwired is the Deliverer and Receiver used until MSG-008 and RT-005 land.
// It logs what arrived, never the payload, and does nothing else.
type Unwired struct{}

// Notify logs the subject.
func (Unwired) Notify(ctx context.Context, c *Conn, m nats.Msg) {
	slog.DebugContext(ctx, "runtime message not delivered: no deliverer wired",
		"runtime_id", c.RuntimeID, "subject", m.Subject)
}

// Receive logs the frame type.
func (Unwired) Receive(ctx context.Context, c *Conn, f contracts.Frame) error {
	slog.DebugContext(ctx, "runtime frame ignored: no receiver wired",
		"runtime_id", c.RuntimeID, "type", f.Type)
	return nil
}

var errClosed = errors.New("gateway: connection closed")
