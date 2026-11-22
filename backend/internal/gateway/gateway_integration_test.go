//go:build integration

package gateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/gateway"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/natstest"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
	"github.com/iykeevans/konduite/packages/contracts"
)

type allowAll struct{}

func (allowAll) Can(context.Context, auth.Principal, authz.Action, authz.Resource) error { return nil }

func TestConnectedRuntimeReceivesItsSubjectFromNATS(t *testing.T) {
	ctx := t.Context()
	pool := dbtest.New(t)
	bus := natstest.New(t)
	if err := bus.Client.EnsureStreams(ctx); err != nil {
		t.Fatal(err)
	}

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	var workspaceID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
		"acme-"+uuid.NewString()[:8], owner).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	rts := runtimes.NewService(runtimes.NewPostgresRepository(pool), allowAll{}, key)
	reg, err := rts.Register(ctx, auth.Principal{Kind: auth.KindUser, ID: owner}, runtimes.RegisterInput{WorkspaceID: workspaceID, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rts.Register(ctx, auth.Principal{Kind: auth.KindUser, ID: owner}, runtimes.RegisterInput{WorkspaceID: workspaceID, Name: "desktop"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := rts.Exchange(ctx, reg.RefreshCredential)
	if err != nil {
		t.Fatal(err)
	}

	deliverer := &fakeDeliverer{got: make(chan notified, 4)}
	received := make(chan contracts.Frame, 4)
	gw := gateway.New(gateway.Config{
		Verify:     func(token string) (runtimes.Claims, error) { return runtimes.VerifyAccessToken(pub, token) },
		Agents:     agents.NewService(agents.NewPostgresRepository(pool)),
		Subscriber: bus.Client,
		Deliverer:  deliverer,
		Receiver:   fakeReceiver{received},
	})
	r := chi.NewRouter()
	gw.Routes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })

	c, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+gateway.Path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + tok.Token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	send(t, c, helloFrame(t))
	send(t, c, frame(t, "runtime.heartbeat", map[string]any{}))
	recv(t, received, "heartbeat after hello")

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agents WHERE runtime_id = $1 AND provider = 'echo' AND name = 'echo'`,
		reg.RuntimeID).Scan(&status); err != nil || status != "online" {
		t.Fatalf("echo agent status = %q, %v; want online", status, err)
	}

	if err := bus.Client.Publish(ctx, "agenthub.rt."+other.RuntimeID.String()+".deliver", "evt-other", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	mine := "agenthub.rt." + reg.RuntimeID.String() + ".deliver"
	if err := bus.Client.Publish(ctx, mine, "evt-mine", json.RawMessage(`{"delivery_id":"d1"}`)); err != nil {
		t.Fatal(err)
	}
	got := recv(t, deliverer.got, "Notify")
	if got.msg.Subject != mine || got.msg.MsgID != "evt-mine" || got.conn.RuntimeID != reg.RuntimeID {
		t.Errorf("notified %+v for runtime %s; want %s for %s", got.msg, got.conn.RuntimeID, mine, reg.RuntimeID)
	}
}
