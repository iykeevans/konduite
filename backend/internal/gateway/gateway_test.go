package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/gateway"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
	"github.com/iykeevans/konduite/packages/contracts"
)

const wait = 5 * time.Second

type syncCall struct {
	rt    agents.Runtime
	hello contracts.RuntimeHello
}

type fakeAgents struct {
	calls chan syncCall
	err   error
}

func (f *fakeAgents) SyncFromHello(_ context.Context, rt agents.Runtime, hello contracts.RuntimeHello) ([]agents.Agent, error) {
	f.calls <- syncCall{rt, hello}
	return nil, f.err
}

type fakeBus struct {
	mu           sync.Mutex
	subs         map[string][]func(nats.Msg)
	subscribed   chan string
	unsubscribed chan string
}

func newFakeBus() *fakeBus {
	return &fakeBus{subs: map[string][]func(nats.Msg){}, subscribed: make(chan string, 8), unsubscribed: make(chan string, 8)}
}

func (b *fakeBus) Subscribe(subject string, fn func(nats.Msg)) (func() error, error) {
	b.mu.Lock()
	b.subs[subject] = append(b.subs[subject], fn)
	idx := len(b.subs[subject]) - 1
	b.mu.Unlock()
	b.subscribed <- subject
	return func() error {
		b.mu.Lock()
		b.subs[subject][idx] = nil
		b.mu.Unlock()
		b.unsubscribed <- subject
		return nil
	}, nil
}

func (b *fakeBus) publish(subject string, m nats.Msg) {
	b.mu.Lock()
	fns := append([]func(nats.Msg){}, b.subs[subject]...)
	b.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(m)
		}
	}
}

type notified struct {
	conn *gateway.Conn
	msg  nats.Msg
}

type fakeDeliverer struct {
	got  chan notified
	push *contracts.Frame
}

func (d *fakeDeliverer) Notify(ctx context.Context, c *gateway.Conn, m nats.Msg) {
	if d.push != nil {
		_ = c.Send(ctx, *d.push)
	}
	d.got <- notified{c, m}
}

type fakeReceiver struct{ got chan contracts.Frame }

func (r fakeReceiver) Receive(_ context.Context, _ *gateway.Conn, f contracts.Frame) error {
	r.got <- f
	return nil
}

type harness struct {
	gw        *gateway.Gateway
	srv       *httptest.Server
	agents    *fakeAgents
	bus       *fakeBus
	deliverer *fakeDeliverer
	received  chan contracts.Frame
	mu        sync.Mutex
	claims    map[string]runtimes.Claims
}

func newHarness(t *testing.T, tweak func(*gateway.Config)) *harness {
	t.Helper()
	h := &harness{
		agents:    &fakeAgents{calls: make(chan syncCall, 8)},
		bus:       newFakeBus(),
		deliverer: &fakeDeliverer{got: make(chan notified, 8)},
		received:  make(chan contracts.Frame, 8),
		claims:    map[string]runtimes.Claims{},
	}
	cfg := gateway.Config{
		Verify: func(token string) (runtimes.Claims, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			c, ok := h.claims[token]
			if !ok {
				return runtimes.Claims{}, errors.New("bad token")
			}
			return c, nil
		},
		Agents:     h.agents,
		Subscriber: h.bus,
		Deliverer:  h.deliverer,
		Receiver:   fakeReceiver{h.received},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	h.gw = gateway.New(cfg)
	r := chi.NewRouter()
	h.gw.Routes(r)
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		_ = h.gw.Shutdown(ctx)
	})
	return h
}

func (h *harness) runtime(token string) runtimes.Claims {
	c := runtimes.Claims{RuntimeID: uuid.New(), WorkspaceID: uuid.New(), OwnerID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)}
	h.mu.Lock()
	h.claims[token] = c
	h.mu.Unlock()
	return c
}

func (h *harness) url() string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + gateway.Path
}

func (h *harness) dial(t *testing.T) *websocket.Conn {
	const token = "tok"
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	c, _, err := websocket.Dial(ctx, h.url(), &websocket.DialOptions{ //nolint:bodyclose // a successful upgrade has no body to close
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func frame(t *testing.T, typ string, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(contracts.Frame{ID: uuid.NewString(), Type: typ, Version: 1, SentAt: time.Now().UTC(), Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func helloFrame(t *testing.T) []byte {
	return frame(t, "runtime.hello", contracts.RuntimeHello{
		RuntimeVersion: "0.1.0",
		Agents:         []contracts.AnnouncedAgent{{Provider: "echo", Name: "echo"}},
	})
}

func send(t *testing.T, c *websocket.Conn, b []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// closeCode reads until the server closes the connection and returns the
// close code it sent.
func closeCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// connected dials as a fresh runtime, says hello and waits until the
// gateway has subscribed to the runtime's subject.
func (h *harness) connected(t *testing.T) (*websocket.Conn, runtimes.Claims) {
	t.Helper()
	const token = "tok"
	h.mu.Lock()
	claims, ok := h.claims[token]
	h.mu.Unlock()
	if !ok {
		claims = h.runtime(token)
	}
	c := h.dial(t)
	send(t, c, helloFrame(t))
	recv(t, h.agents.calls, "SyncFromHello")
	if got := recv(t, h.bus.subscribed, "subscribe"); got != gateway.Subject(claims.RuntimeID) {
		t.Fatalf("subscribed to %q, want %q", got, gateway.Subject(claims.RuntimeID))
	}
	return c, claims
}

func TestConnectRejectsBadTokensBeforeUpgrading(t *testing.T) {
	h := newHarness(t, nil)
	h.runtime("good")
	for name, header := range map[string]string{
		"missing":    "",
		"not bearer": "Basic good",
		"unknown":    "Bearer forged",
	} {
		t.Run(name, func(t *testing.T) {
			hdr := http.Header{}
			if header != "" {
				hdr.Set("Authorization", header)
			}
			_, resp, err := websocket.Dial(t.Context(), h.url(), &websocket.DialOptions{HTTPHeader: hdr})
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
			}
			if err == nil {
				t.Fatal("dial succeeded")
			}
			if resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("response = %v, want 401", resp)
			}
		})
	}
	if len(h.agents.calls) != 0 {
		t.Error("SyncFromHello called for a rejected connection")
	}
}

func TestConnectClosesWhenHelloDoesNotArriveInTime(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.HelloTimeout = 50 * time.Millisecond })
	h.runtime("tok")
	c := h.dial(t)
	if got := closeCode(t, c); got != gateway.StatusHelloTimeout {
		t.Errorf("close code = %d, want %d", got, gateway.StatusHelloTimeout)
	}
}

func TestConnectClosesWithPolicyViolationOnABadFirstFrame(t *testing.T) {
	cases := map[string]func(t *testing.T) []byte{
		"not json":         func(*testing.T) []byte { return []byte("hello?") },
		"envelope invalid": func(*testing.T) []byte { return []byte(`{"type":"runtime.hello"}`) },
		"payload invalid":  func(t *testing.T) []byte { return frame(t, "runtime.hello", map[string]any{"agents": []any{}}) },
		"unknown type":     func(t *testing.T) []byte { return frame(t, "runtime.nope", map[string]any{}) },
		"heartbeat first":  func(t *testing.T) []byte { return frame(t, "runtime.heartbeat", map[string]any{}) },
		"tenant in payload": func(t *testing.T) []byte {
			return frame(t, "runtime.hello", map[string]any{"runtime_version": "1", "agents": []any{}, "workspace_id": uuid.NewString()})
		},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.runtime("tok")
			c := h.dial(t)
			send(t, c, raw(t))
			if got := closeCode(t, c); got != websocket.StatusPolicyViolation {
				t.Errorf("close code = %d, want 1008", got)
			}
			if len(h.agents.calls) != 0 {
				t.Error("SyncFromHello called for an invalid hello")
			}
		})
	}
}

func TestConnectClosesOnABinaryMessage(t *testing.T) {
	h := newHarness(t, nil)
	h.runtime("tok")
	c := h.dial(t)
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, helloFrame(t)); err != nil {
		t.Fatal(err)
	}
	if got := closeCode(t, c); got != websocket.StatusPolicyViolation {
		t.Errorf("close code = %d, want 1008", got)
	}
}

func TestHelloSyncsAgentsUnderTheTokensRuntime(t *testing.T) {
	h := newHarness(t, nil)
	claims := h.runtime("tok")
	c := h.dial(t)
	send(t, c, helloFrame(t))

	call := recv(t, h.agents.calls, "SyncFromHello")
	if call.rt.ID != claims.RuntimeID || call.rt.WorkspaceID != claims.WorkspaceID {
		t.Errorf("synced runtime %+v, want ids from the token %+v", call.rt, claims)
	}
	if call.hello.RuntimeVersion != "0.1.0" || len(call.hello.Agents) != 1 || call.hello.Agents[0].Provider != "echo" {
		t.Errorf("hello = %+v", call.hello)
	}
}

func TestHelloFailuresCloseTheConnection(t *testing.T) {
	cases := map[string]struct {
		err  error
		want websocket.StatusCode
	}{
		"runtime gone":     {errs.NotFound("runtime not found"), gateway.StatusRuntimeNotFound},
		"duplicate agents": {errs.Invalid("invalid runtime hello", nil), websocket.StatusPolicyViolation},
		"storage down":     {errors.New("db down"), websocket.StatusInternalError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.agents.err = tc.err
			h.runtime("tok")
			c := h.dial(t)
			send(t, c, helloFrame(t))
			if got := closeCode(t, c); got != tc.want {
				t.Errorf("close code = %d, want %d", got, tc.want)
			}
			if len(h.bus.subscribed) != 0 {
				t.Error("subscribed after a failed hello")
			}
		})
	}
}

func TestFramesAfterHelloReachTheReceiver(t *testing.T) {
	h := newHarness(t, nil)
	c, _ := h.connected(t)
	send(t, c, frame(t, "runtime.heartbeat", map[string]any{}))
	if f := recv(t, h.received, "heartbeat"); f.Type != "runtime.heartbeat" {
		t.Errorf("received %q", f.Type)
	}
}

func TestInvalidFrameAfterHelloCloses(t *testing.T) {
	for name, raw := range map[string]func(t *testing.T) []byte{
		"invalid ack":  func(t *testing.T) []byte { return frame(t, "runtime.event_ack", map[string]any{"event_id": "nope"}) },
		"second hello": helloFrame,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			c, claims := h.connected(t)
			send(t, c, raw(t))
			if got := closeCode(t, c); got != websocket.StatusPolicyViolation {
				t.Errorf("close code = %d, want 1008", got)
			}
			if got := recv(t, h.bus.unsubscribed, "unsubscribe"); got != gateway.Subject(claims.RuntimeID) {
				t.Errorf("unsubscribed %q", got)
			}
		})
	}
}

func TestMessagesOnTheRuntimeSubjectReachTheDeliverer(t *testing.T) {
	h := newHarness(t, nil)
	push := contracts.Frame{ID: uuid.NewString(), Type: "runtime.heartbeat", Version: 1, SentAt: time.Now().UTC(), Data: json.RawMessage(`{}`)}
	h.deliverer.push = &push
	c, claims := h.connected(t)

	subject := "agenthub.rt." + claims.RuntimeID.String() + ".deliver"
	h.bus.publish(gateway.Subject(claims.RuntimeID), nats.Msg{Subject: subject, MsgID: "evt-1", Data: []byte(`{"x":1}`)})

	got := recv(t, h.deliverer.got, "Notify")
	if got.conn.RuntimeID != claims.RuntimeID || got.conn.WorkspaceID != claims.WorkspaceID {
		t.Errorf("notified conn for runtime %s, want %s", got.conn.RuntimeID, claims.RuntimeID)
	}
	if got.msg.Subject != subject || got.msg.MsgID != "evt-1" || string(got.msg.Data) != `{"x":1}` {
		t.Errorf("msg = %+v", got.msg)
	}

	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var f contracts.Frame
	if err := json.Unmarshal(data, &f); err != nil || f.ID != push.ID {
		t.Errorf("runtime got %s, want frame %s", data, push.ID)
	}

	if err := c.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, h.bus.unsubscribed, "unsubscribe"); got != gateway.Subject(claims.RuntimeID) {
		t.Errorf("unsubscribed %q", got)
	}
}

func TestSendRejectsAFrameThatBreaksItsContract(t *testing.T) {
	h := newHarness(t, nil)
	_, claims := h.connected(t)
	h.bus.publish(gateway.Subject(claims.RuntimeID), nats.Msg{Subject: "s"})
	n := recv(t, h.deliverer.got, "Notify")
	err := n.conn.Send(t.Context(), contracts.Frame{ID: "not-a-uuid", Type: "runtime.heartbeat", Version: 1, SentAt: time.Now(), Data: json.RawMessage(`{}`)})
	if err == nil {
		t.Error("Send accepted an invalid frame")
	}
}

func TestASecondConnectionReplacesTheFirst(t *testing.T) {
	h := newHarness(t, nil)
	first, claims := h.connected(t)
	second, _ := h.connected(t)

	if got := closeCode(t, first); got != gateway.StatusReplaced {
		t.Errorf("first close code = %d, want %d", got, gateway.StatusReplaced)
	}
	recv(t, h.bus.unsubscribed, "first unsubscribe")

	h.bus.publish(gateway.Subject(claims.RuntimeID), nats.Msg{Subject: "s"})
	n := recv(t, h.deliverer.got, "Notify")
	if err := n.conn.Send(t.Context(), contracts.Frame{ID: uuid.NewString(), Type: "runtime.heartbeat", Version: 1, SentAt: time.Now().UTC(), Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("send on the surviving connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	if _, _, err := second.Read(ctx); err != nil {
		t.Fatalf("second connection: %v", err)
	}
}

func TestShutdownClosesOpenConnectionsAndRefusesNewOnes(t *testing.T) {
	h := newHarness(t, nil)
	c, _ := h.connected(t)
	pending := h.dial(t)

	codes := make(chan websocket.StatusCode, 2)
	go func() { codes <- closeCode(t, c) }()
	go func() { codes <- closeCode(t, pending) }()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	if err := h.gw.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for range 2 {
		if got := recv(t, codes, "close"); got != websocket.StatusGoingAway {
			t.Errorf("close code = %d, want 1001", got)
		}
	}

	late, _, err := websocket.Dial( //nolint:bodyclose // a successful upgrade has no body to close
		ctx, h.url(), &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer tok"}},
		})
	if err == nil {
		if got := closeCode(t, late); got != websocket.StatusGoingAway {
			t.Errorf("connection after shutdown closed with %d, want 1001", got)
		}
	}
}

func TestAPeerThatStopsAnsweringPingsIsDropped(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) {
		c.PingInterval = 20 * time.Millisecond
		c.PongTimeout = 50 * time.Millisecond
	})
	_, claims := h.connected(t)
	if got := recv(t, h.bus.unsubscribed, "unsubscribe after a missed pong"); got != gateway.Subject(claims.RuntimeID) {
		t.Errorf("unsubscribed %q", got)
	}
}
