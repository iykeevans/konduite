package connection_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
	"github.com/iykeevans/konduite/runtime/internal/connection"
)

const wait = 5 * time.Second

// server is a stand-in for the API's connect endpoint. Each accepted
// connection is handed to the test on conns after its hello is read.
type server struct {
	*httptest.Server
	conns  chan *peer
	header chan http.Header
	urls   chan string
}

type peer struct {
	ws    *websocket.Conn
	hello contracts.Frame
}

func newServer(t *testing.T, token string) *server {
	t.Helper()
	s := &server{conns: make(chan *peer, 4), header: make(chan http.Header, 4), urls: make(chan string, 4)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.urls <- r.URL.String()
		s.header <- r.Header.Clone()
		if r.URL.Path != connection.Path || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		_, raw, err := ws.Read(ctx)
		if err != nil {
			_ = ws.CloseNow()
			return
		}
		f, err := validate.Frame(raw)
		if err != nil {
			_ = ws.Close(websocket.StatusPolicyViolation, "invalid hello")
			return
		}
		s.conns <- &peer{ws: ws, hello: f}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) accept(t *testing.T) *peer {
	t.Helper()
	select {
	case p := <-s.conns:
		t.Cleanup(func() { _ = p.ws.CloseNow() })
		return p
	case <-time.After(wait):
		t.Fatal("no connection reached the server")
		return nil
	}
}

func opts(s *server, token string) connection.Options {
	return connection.Options{
		ServerURL: s.URL,
		Token:     token,
		Hello: contracts.RuntimeHello{
			RuntimeVersion: "1.2.3",
			Agents:         []contracts.AnnouncedAgent{{Provider: "echo", Name: "echo"}},
		},
	}
}

func dial(t *testing.T, o connection.Options) *connection.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	c, err := connection.Dial(ctx, o)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c
}

// run starts c.Run and returns a channel with its result.
func run(ctx context.Context, c *connection.Conn) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return done
}

func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		t.Fatal("Run did not return")
		return nil
	}
}

func serverCloseCode(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func frame(t *testing.T, typ string, data any) contracts.Frame {
	t.Helper()
	f, err := connection.NewFrame(typ, data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func write(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, ok := v.([]byte)
	if !ok {
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func TestDialAuthenticatesInTheHeaderAndSendsHello(t *testing.T) {
	s := newServer(t, "rat.secret")
	dial(t, opts(s, "rat.secret"))
	p := s.accept(t)

	if u := <-s.urls; strings.Contains(u, "rat.secret") || u != connection.Path {
		t.Errorf("connect URL = %q; the token must only travel in the header", u)
	}
	if got := (<-s.header).Get("Authorization"); got != "Bearer rat.secret" {
		t.Errorf("Authorization = %q", got)
	}
	if p.hello.Type != "runtime.hello" || p.hello.Version != 1 {
		t.Fatalf("first frame = %s v%d", p.hello.Type, p.hello.Version)
	}
	var hello contracts.RuntimeHello
	if err := json.Unmarshal(p.hello.Data, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.RuntimeVersion != "1.2.3" || len(hello.Agents) != 1 || hello.Agents[0].Provider != "echo" {
		t.Errorf("hello = %+v", hello)
	}
}

func TestDialReportsARejectedToken(t *testing.T) {
	s := newServer(t, "rat.secret")
	_, err := connection.Dial(t.Context(), opts(s, "rat.expired"))
	if !errors.Is(err, connection.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestDialRefusesAnInvalidHello(t *testing.T) {
	s := newServer(t, "tok")
	o := opts(s, "tok")
	o.Hello.RuntimeVersion = ""
	if _, err := connection.Dial(t.Context(), o); err == nil {
		t.Fatal("Dial sent a hello that breaks its contract")
	}
	select {
	case u := <-s.urls:
		t.Errorf("Dial reached the server (%s) with an invalid hello", u)
	default:
	}
}

func TestDialRejectsANonHTTPServerURL(t *testing.T) {
	for _, u := range []string{"ftp://example.com", "not a url", ""} {
		o := connection.Options{ServerURL: u, Token: "tok", Hello: contracts.RuntimeHello{RuntimeVersion: "1", Agents: []contracts.AnnouncedAgent{}}}
		if _, err := connection.Dial(t.Context(), o); err == nil {
			t.Errorf("Dial(%q) succeeded", u)
		}
	}
}

func TestRunHandsServerFramesToTheHandler(t *testing.T) {
	s := newServer(t, "tok")
	got := make(chan contracts.Frame, 1)
	o := opts(s, "tok")
	o.Handler = func(_ context.Context, f contracts.Frame) error {
		got <- f
		return nil
	}
	c := dial(t, o)
	p := s.accept(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := run(ctx, c)

	want := frame(t, "runtime.heartbeat", map[string]any{})
	write(t, p.ws, want)
	select {
	case f := <-got:
		if f.ID != want.ID || f.Type != want.Type {
			t.Errorf("handler got %+v", f)
		}
	case <-time.After(wait):
		t.Fatal("handler never called")
	}

	codes := make(chan websocket.StatusCode, 1)
	go func() { codes <- serverCloseCode(t, p.ws) }()
	cancel()
	if err := result(t, done); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
	if code := <-codes; code != websocket.StatusNormalClosure {
		t.Errorf("server saw close %d, want 1000", code)
	}
}

func TestRunClosesWithPolicyViolationOnAnInvalidServerFrame(t *testing.T) {
	s := newServer(t, "tok")
	c := dial(t, opts(s, "tok"))
	p := s.accept(t)
	done := run(t.Context(), c)

	write(t, p.ws, []byte(`{"type":"runtime.heartbeat"}`))
	if code := serverCloseCode(t, p.ws); code != websocket.StatusPolicyViolation {
		t.Errorf("server saw close %d, want 1008", code)
	}
	if err := result(t, done); err == nil {
		t.Error("Run returned nil after an invalid frame")
	}
}

func TestRunSurfacesTheServersCloseCode(t *testing.T) {
	s := newServer(t, "tok")
	c := dial(t, opts(s, "tok"))
	p := s.accept(t)
	done := run(t.Context(), c)

	_ = p.ws.Close(connection.StatusReplaced, "replaced by a newer connection for this runtime")
	err := result(t, done)
	var d *connection.Disconnected
	if !errors.As(err, &d) || d.Code != connection.StatusReplaced {
		t.Fatalf("Run = %v, want Disconnected with code %d", err, connection.StatusReplaced)
	}
	if !strings.Contains(err.Error(), "replaced") {
		t.Errorf("error %q does not say why", err)
	}
}

func TestRunNoticesAServerThatStopsAnsweringPings(t *testing.T) {
	s := newServer(t, "tok")
	o := opts(s, "tok")
	o.PingInterval = 20 * time.Millisecond
	o.PongTimeout = 50 * time.Millisecond
	c := dial(t, o)
	s.accept(t)

	err := result(t, run(t.Context(), c))
	var d *connection.Disconnected
	if !errors.As(err, &d) || d.Code != -1 {
		t.Fatalf("Run = %v, want a lost connection", err)
	}
}

func TestRunAnswersServerPings(t *testing.T) {
	s := newServer(t, "tok")
	c := dial(t, opts(s, "tok"))
	p := s.accept(t)
	run(t.Context(), c)

	readCtx := p.ws.CloseRead(t.Context())
	ctx, cancel := context.WithTimeout(readCtx, wait)
	defer cancel()
	if err := p.ws.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestSendDeliversValidFramesAndRefusesInvalidOnes(t *testing.T) {
	s := newServer(t, "tok")
	c := dial(t, opts(s, "tok"))
	p := s.accept(t)
	run(t.Context(), c)

	bad := frame(t, "runtime.event_ack", map[string]any{"event_id": "not-a-uuid"})
	if err := c.Send(t.Context(), bad); err == nil {
		t.Error("Send accepted a frame that breaks its contract")
	}
	good := frame(t, "runtime.heartbeat", map[string]any{})
	if err := c.Send(t.Context(), good); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	_, raw, err := p.ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := validate.Frame(raw)
	if err != nil || f.ID != good.ID {
		t.Errorf("server got %s (%v), want frame %s", raw, err, good.ID)
	}
}
