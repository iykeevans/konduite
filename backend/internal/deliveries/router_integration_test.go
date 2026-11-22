//go:build integration

package deliveries_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/deliveries"
	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/messages"
	"github.com/iykeevans/konduite/backend/internal/outbox"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/platform/natstest"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
)

const waitFor = 20 * time.Second

type world struct {
	t            *testing.T
	pool         *pgxpool.Pool
	srv          *natstest.Server
	relay        *outbox.Relay
	handled      chan error
	workspace    uuid.UUID
	conversation uuid.UUID
	alice, bob   uuid.UUID
	rt1, rt2     uuid.UUID
	agentA       uuid.UUID
	agentB       uuid.UUID
}

func setup(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, pool: dbtest.New(t), srv: natstest.New(t), handled: make(chan error, 16),
		alice: uuid.New(), bob: uuid.New()}
	if err := w.srv.Client.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.relay = outbox.NewRelay(w.pool, w.srv.Client, outbox.Config{})

	w.scan(`INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
		[]any{"acme-" + uuid.NewString()[:8], w.alice}, &w.workspace)
	w.rt1 = w.runtime()
	w.rt2 = w.runtime()
	w.agentA = w.agent(w.rt1, "a")
	w.agentB = w.agent(w.rt1, "b")
	w.scan(`INSERT INTO conversations (workspace_id, created_by) VALUES ($1, $2) RETURNING id`,
		[]any{w.workspace, w.alice}, &w.conversation)
	w.join(conversations.KindUser, w.alice)
	w.join(conversations.KindUser, w.bob)
	w.join(conversations.KindAgent, w.agentA)
	w.join(conversations.KindAgent, w.agentB)
	w.join(conversations.KindRuntime, w.rt2)

	router := deliveries.NewRouter(w.pool, nil)
	spec := deliveries.ConsumerSpec(nil)
	spec.Backoff = []time.Duration{10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- w.srv.Client.Consume(ctx, spec, func(ctx context.Context, d nats.Delivery) error {
			err := router.Handle(ctx, d)
			w.handled <- err
			return err
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Consume returned %v", err)
			}
		case <-time.After(waitFor):
			t.Error("Consume did not return after cancel")
		}
	})
	return w
}

func (w *world) scan(sql string, args []any, dest *uuid.UUID) {
	w.t.Helper()
	if err := w.pool.QueryRow(w.t.Context(), sql, args...).Scan(dest); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) runtime() uuid.UUID {
	var id uuid.UUID
	w.scan(`INSERT INTO agent_runtime_instances (workspace_id, owner_id, name) VALUES ($1, $2, 'laptop') RETURNING id`,
		[]any{w.workspace, w.alice}, &id)
	return id
}

func (w *world) agent(runtime uuid.UUID, name string) uuid.UUID {
	var id uuid.UUID
	w.scan(`INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'claude_code', $3) RETURNING id`,
		[]any{w.workspace, runtime, name}, &id)
	return id
}

func (w *world) join(kind conversations.ParticipantKind, id uuid.UUID) {
	w.t.Helper()
	if _, err := w.pool.Exec(w.t.Context(),
		`INSERT INTO conversation_participants (conversation_id, participant_type, participant_id) VALUES ($1, $2, $3)`,
		w.conversation, kind, id); err != nil {
		w.t.Fatal(err)
	}
}

// send stores a message and its message.created event the way MSG-003
// does, and returns the message id and the event payload.
func (w *world) send(kind conversations.ParticipantKind, sender uuid.UUID) (uuid.UUID, []byte) {
	w.t.Helper()
	ctx := w.t.Context()
	var (
		m       messages.Message
		eventID uuid.UUID
	)
	err := db.WithTx(ctx, w.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, _, err = messages.AppendTx(ctx, tx, messages.NewMessage{
			ConversationID: w.conversation,
			Sender:         messages.Sender{Kind: kind, ID: sender},
			IdempotencyKey: uuid.NewString(),
			Type:           messages.TypeText,
			Content:        "hello",
		})
		if err != nil {
			return err
		}
		eventID, err = events.Enqueue(ctx, store.New(tx), messages.CreatedEvent(m, w.workspace, nil))
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	var payload []byte
	if err := w.pool.QueryRow(ctx, `SELECT payload FROM outbox_events WHERE id = $1`, eventID).Scan(&payload); err != nil {
		w.t.Fatal(err)
	}
	return m.ID, payload
}

func (w *world) relayAll() {
	w.t.Helper()
	if _, err := w.relay.PublishPending(w.t.Context()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) handledOnce() error {
	w.t.Helper()
	select {
	case err := <-w.handled:
		return err
	case <-time.After(waitFor):
		w.t.Fatal("the router never handled the event")
		return nil
	}
}

func (w *world) deliveries(message uuid.UUID) []store.MessageDelivery {
	w.t.Helper()
	rows, err := store.New(w.pool).ListMessageDeliveries(w.t.Context(), message)
	if err != nil {
		w.t.Fatal(err)
	}
	return rows
}

func (w *world) wakeUpRows() int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(w.t.Context(), `SELECT count(*) FROM outbox_events WHERE event_type = 'runtime.deliver'`).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) subscribeRuntimes() <-chan *natsgo.Msg {
	w.t.Helper()
	nc, err := natsgo.Connect(w.srv.URL)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(nc.Close)
	ch := make(chan *natsgo.Msg, 16)
	if _, err := nc.ChanSubscribe("agenthub.rt.>", ch); err != nil {
		w.t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		w.t.Fatal(err)
	}
	return ch
}

type recipient struct {
	kind    string
	id      uuid.UUID
	runtime *uuid.UUID
}

func recipients(rows []store.MessageDelivery) []recipient {
	out := make([]recipient, len(rows))
	for i, r := range rows {
		out[i] = recipient{r.RecipientType, r.RecipientID, r.RuntimeID}
	}
	return out
}

func sameRecipients(got, want []recipient) bool {
	key := func(r recipient) string {
		rt := ""
		if r.runtime != nil {
			rt = r.runtime.String()
		}
		return r.kind + r.id.String() + rt
	}
	g, wk := make([]string, len(got)), make([]string, len(want))
	for i := range got {
		g[i] = key(got[i])
	}
	for i := range want {
		wk[i] = key(want[i])
	}
	slices.Sort(g)
	slices.Sort(wk)
	return slices.Equal(g, wk)
}

func TestMessageCreatedWritesDeliveriesAndWakesEachRuntimeOnce(t *testing.T) {
	w := setup(t)
	wake := w.subscribeRuntimes()
	msg, _ := w.send(conversations.KindUser, w.alice)

	w.relayAll()
	if err := w.handledOnce(); err != nil {
		t.Fatalf("Handle = %v", err)
	}

	rows := w.deliveries(msg)
	want := []recipient{
		{"user", w.bob, nil},
		{"agent", w.agentA, &w.rt1},
		{"agent", w.agentB, &w.rt1},
		{"runtime", w.rt2, &w.rt2},
	}
	if !sameRecipients(recipients(rows), want) {
		t.Fatalf("deliveries = %+v, want %+v", recipients(rows), want)
	}
	for _, r := range rows {
		if r.Status != "pending" || r.SequenceNumber != 1 || r.ConversationID != w.conversation || r.WorkspaceID != w.workspace {
			t.Errorf("delivery %+v, want pending, seq 1, in the conversation and workspace", r)
		}
	}

	w.relayAll()
	got := map[string]contracts.RuntimeDeliver{}
	for range 2 {
		var m *natsgo.Msg
		select {
		case m = <-wake:
		case <-time.After(waitFor):
			t.Fatalf("got %d wake-ups, want 2", len(got))
		}
		env, err := validate.Event(m.Data)
		if err != nil {
			t.Fatalf("wake-up fails its schema: %v", err)
		}
		var p contracts.RuntimeDeliver
		if err := json.Unmarshal(env.Data, &p); err != nil {
			t.Fatal(err)
		}
		if env.Type != "runtime.deliver" || p.MessageID != msg.String() || p.ConversationID != w.conversation.String() || p.SequenceNumber != 1 {
			t.Errorf("wake-up %s = %+v", env.Type, p)
		}
		got[m.Subject] = p
	}
	byRuntime := map[uuid.UUID][]string{}
	for _, r := range rows {
		if r.RuntimeID != nil {
			byRuntime[*r.RuntimeID] = append(byRuntime[*r.RuntimeID], r.ID.String())
		}
	}
	for _, rt := range []uuid.UUID{w.rt1, w.rt2} {
		p, ok := got[deliveries.NotifySubject(rt)]
		if !ok {
			t.Errorf("no wake-up on %s", deliveries.NotifySubject(rt))
			continue
		}
		if p.RuntimeID != rt.String() || !sameStrings(p.DeliveryIds, byRuntime[rt]) {
			t.Errorf("wake-up for %s = %+v, want deliveries %v", rt, p, byRuntime[rt])
		}
	}
}

func sameStrings(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func TestRedeliveredMessageCreatedChangesNothing(t *testing.T) {
	w := setup(t)
	msg, payload := w.send(conversations.KindUser, w.alice)
	w.relayAll()
	if err := w.handledOnce(); err != nil {
		t.Fatal(err)
	}
	before := w.deliveries(msg)

	if err := w.srv.Client.Publish(t.Context(), events.Subject("message.created"), "redelivery-"+uuid.NewString(), payload); err != nil {
		t.Fatal(err)
	}
	if err := w.handledOnce(); err != nil {
		t.Fatalf("second Handle = %v, want nil", err)
	}

	after := w.deliveries(msg)
	if len(after) != len(before) || len(after) != 4 {
		t.Errorf("deliveries %d before, %d after; want 4 both times", len(before), len(after))
	}
	if n := w.wakeUpRows(); n != 2 {
		t.Errorf("runtime.deliver events = %d, want 2 (none for the redelivery)", n)
	}
}

func TestAgentSenderIsExcludedAndItsRuntimeStillHearsOtherAgents(t *testing.T) {
	w := setup(t)
	msg, _ := w.send(conversations.KindAgent, w.agentA)
	w.relayAll()
	if err := w.handledOnce(); err != nil {
		t.Fatal(err)
	}

	want := []recipient{
		{"user", w.alice, nil},
		{"user", w.bob, nil},
		{"agent", w.agentB, &w.rt1},
		{"runtime", w.rt2, &w.rt2},
	}
	if got := recipients(w.deliveries(msg)); !sameRecipients(got, want) {
		t.Errorf("deliveries = %+v, want %+v", got, want)
	}
	if n := w.wakeUpRows(); n != 2 {
		t.Errorf("runtime.deliver events = %d, want 2", n)
	}
}

func TestDeletedConversationIsDeadLettered(t *testing.T) {
	w := setup(t)
	_, payload := w.send(conversations.KindUser, w.alice)
	if _, err := w.pool.Exec(t.Context(), `DELETE FROM conversations WHERE id = $1`, w.conversation); err != nil {
		t.Fatal(err)
	}

	if err := w.srv.Client.Publish(t.Context(), events.Subject("message.created"), uuid.NewString(), payload); err != nil {
		t.Fatal(err)
	}
	if err := w.handledOnce(); !nats.IsPermanent(err) {
		t.Fatalf("Handle = %v, want a permanent error", err)
	}

	cons, err := w.srv.JetStream.OrderedConsumer(t.Context(), nats.DLQStream, jetstream.OrderedConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	dl, err := cons.Next(jetstream.FetchMaxWait(waitFor))
	if err != nil {
		t.Fatalf("no dead letter: %v", err)
	}
	if dl.Subject() != "dlq.agenthub.message.created" || dl.Headers().Get(nats.HeaderAttempts) != "1" ||
		dl.Headers().Get(nats.HeaderConsumer) != "message-router" {
		t.Errorf("dead letter on %s, headers %v", dl.Subject(), dl.Headers())
	}
	if n := w.wakeUpRows(); n != 0 {
		t.Errorf("runtime.deliver events = %d, want 0", n)
	}
}
