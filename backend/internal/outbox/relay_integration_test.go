//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	natsgo "github.com/nats-io/nats.go"

	"github.com/iykeevans/konduite/backend/internal/outbox"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/platform/natstest"
)

func envelope(id uuid.UUID, eventType string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": id, "type": eventType, "version": 1, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		"workspace_id": uuid.New(), "correlation_id": uuid.New(), "data": map[string]any{},
	})
	return b
}

func insert(t *testing.T, pool *pgxpool.Pool, eventType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO outbox_events (id, event_type, aggregate_id, payload) VALUES ($1, $2, $3, $4)`,
		id, eventType, uuid.New(), envelope(id, eventType)); err != nil {
		t.Fatal(err)
	}
	return id
}

func unpublished(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func streamMsgs(t *testing.T, srv *natstest.Server) uint64 {
	t.Helper()
	s, err := srv.JetStream.Stream(t.Context(), nats.Stream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

func ensured(t *testing.T) *natstest.Server {
	t.Helper()
	srv := natstest.New(t)
	if err := srv.Client.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	return srv
}

func subscribe(t *testing.T, srv *natstest.Server) <-chan *natsgo.Msg {
	t.Helper()
	nc, err := natsgo.Connect(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	ch := make(chan *natsgo.Msg, 64)
	if _, err := nc.ChanSubscribe("agenthub.>", ch); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	return ch
}

func receive(t *testing.T, ch <-chan *natsgo.Msg, within time.Duration) *natsgo.Msg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(within):
		t.Fatalf("no message within %v", within)
		return nil
	}
}

func TestPublishPendingPublishesPayloadUnchangedAndMarksRows(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	msgs := subscribe(t, srv)
	id := insert(t, pool, "message.created")

	r := outbox.NewRelay(pool, srv.Client, outbox.Config{})
	n, err := r.PublishPending(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("PublishPending = %d, %v; want 1, nil", n, err)
	}

	m := receive(t, msgs, 5*time.Second)
	if m.Subject != "agenthub.message.created" {
		t.Errorf("subject = %q", m.Subject)
	}
	if got := m.Header.Get(natsgo.MsgIdHdr); got != id.String() {
		t.Errorf("Nats-Msg-Id = %q, want %s", got, id)
	}
	var stored []byte
	if err := pool.QueryRow(t.Context(), `SELECT payload FROM outbox_events WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(m.Data) != string(stored) {
		t.Errorf("published %s, want the stored payload %s", m.Data, stored)
	}
	if u := unpublished(t, pool); u != 0 {
		t.Errorf("unpublished = %d after publish, want 0", u)
	}

	if n, err := r.PublishPending(t.Context()); err != nil || n != 0 {
		t.Errorf("second PublishPending = %d, %v; want 0, nil (already published)", n, err)
	}
	if got := streamMsgs(t, srv); got != 1 {
		t.Errorf("stream holds %d messages, want 1", got)
	}
}

func TestPublishPendingUsesTheRowSubjectWhenSet(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	msgs := subscribe(t, srv)
	id := insert(t, pool, "runtime.deliver")
	subject := "agenthub.rt." + uuid.NewString() + ".deliver"
	if _, err := pool.Exec(t.Context(), `UPDATE outbox_events SET subject = $2 WHERE id = $1`, id, subject); err != nil {
		t.Fatal(err)
	}

	if _, err := outbox.NewRelay(pool, srv.Client, outbox.Config{}).PublishPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := receive(t, msgs, 5*time.Second)
	if m.Subject != subject || m.Header.Get(natsgo.MsgIdHdr) != id.String() {
		t.Errorf("published on %q with id %q, want %q with id %s", m.Subject, m.Header.Get(natsgo.MsgIdHdr), subject, id)
	}
}

func TestPublishPendingDrainsMoreThanOneBatch(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	for range 25 {
		insert(t, pool, "message.created")
	}
	n, err := outbox.NewRelay(pool, srv.Client, outbox.Config{BatchSize: 10}).PublishPending(t.Context())
	if err != nil || n != 25 {
		t.Fatalf("PublishPending = %d, %v; want 25, nil", n, err)
	}
	if got := streamMsgs(t, srv); got != 25 {
		t.Errorf("stream holds %d, want 25", got)
	}
}

// A relay that crashed after publishing but before committing leaves the row
// unpublished; the retry reuses the event id, so JetStream drops it.
func TestRepublishAfterCrashBeforeCommitIsDeduped(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	id := insert(t, pool, "message.created")
	var payload []byte
	if err := pool.QueryRow(t.Context(), `SELECT payload FROM outbox_events WHERE id = $1`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := srv.Client.Publish(t.Context(), "agenthub.message.created", id.String(), payload); err != nil {
		t.Fatal(err)
	}

	if _, err := outbox.NewRelay(pool, srv.Client, outbox.Config{}).PublishPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := streamMsgs(t, srv); got != 1 {
		t.Errorf("stream holds %d messages, want 1 (retry deduped by Nats-Msg-Id)", got)
	}
	if u := unpublished(t, pool); u != 0 {
		t.Errorf("unpublished = %d, want 0", u)
	}
}

type countingPublisher struct {
	next outbox.Publisher
	mu   sync.Mutex
	seen map[string]int
}

func (c *countingPublisher) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	c.mu.Lock()
	c.seen[msgID]++
	c.mu.Unlock()
	return c.next.Publish(ctx, subject, msgID, data)
}

func TestConcurrentRelaysClaimEachRowOnce(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	const events = 200
	for range events {
		insert(t, pool, "message.created")
	}
	pub := &countingPublisher{next: srv.Client, seen: map[string]int{}}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := outbox.NewRelay(pool, pub, outbox.Config{BatchSize: 7})
			if _, err := r.PublishPending(t.Context()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if len(pub.seen) != events {
		t.Errorf("published %d distinct events, want %d", len(pub.seen), events)
	}
	for id, n := range pub.seen {
		if n != 1 {
			t.Errorf("event %s published %d times, want 1", id, n)
		}
	}
	if got := streamMsgs(t, srv); got != events {
		t.Errorf("stream holds %d, want %d", got, events)
	}
	if u := unpublished(t, pool); u != 0 {
		t.Errorf("unpublished = %d, want 0", u)
	}
}

type flakyPublisher struct {
	next     outbox.Publisher
	failures atomic.Int32
	attempts chan string
}

func (f *flakyPublisher) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	defer func() { f.attempts <- msgID }()
	if f.failures.Add(-1) >= 0 {
		return errors.New("nats down")
	}
	return f.next.Publish(ctx, subject, msgID, data)
}

func TestPublishFailureLeavesRowUnpublished(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	insert(t, pool, "message.created")
	pub := &flakyPublisher{next: srv.Client, attempts: make(chan string, 8)}
	pub.failures.Store(1)
	r := outbox.NewRelay(pool, pub, outbox.Config{})

	if _, err := r.PublishPending(t.Context()); err == nil {
		t.Fatal("PublishPending = nil error, want the publish failure")
	}
	if u := unpublished(t, pool); u != 1 {
		t.Fatalf("unpublished = %d after failed publish, want 1", u)
	}
	if n, err := r.PublishPending(t.Context()); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v; want 1, nil", n, err)
	}
	if u := unpublished(t, pool); u != 0 {
		t.Errorf("unpublished = %d after retry, want 0", u)
	}
}

type failSecond struct {
	next  outbox.Publisher
	calls int
}

func (f *failSecond) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	f.calls++
	if f.calls == 2 {
		return errors.New("nats down")
	}
	return f.next.Publish(ctx, subject, msgID, data)
}

func TestPublishFailureMidBatchKeepsTheEventsAlreadyPublished(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	first := insert(t, pool, "message.created")
	if _, err := pool.Exec(t.Context(), `UPDATE outbox_events SET created_at = now() - interval '1 minute' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	insert(t, pool, "message.created")

	if _, err := outbox.NewRelay(pool, &failSecond{next: srv.Client}, outbox.Config{}).PublishPending(t.Context()); err == nil {
		t.Fatal("want the second publish's error")
	}
	var published bool
	if err := pool.QueryRow(t.Context(), `SELECT published_at IS NOT NULL FROM outbox_events WHERE id = $1`, first).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if u := unpublished(t, pool); !published || u != 1 {
		t.Errorf("first published = %v, unpublished = %d; want true, 1", published, u)
	}
}

func runRelay(t *testing.T, r *outbox.Relay) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v, want nil on cancel", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
}

// The poll interval is an hour, so only LISTEN/NOTIFY can deliver these.
func TestRunPublishesOnNotifyWithoutWaitingForThePoll(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	msgs := subscribe(t, srv)
	runRelay(t, outbox.NewRelay(pool, srv.Client, outbox.Config{PollInterval: time.Hour}))

	a := insert(t, pool, "message.created")
	if m := receive(t, msgs, 10*time.Second); m.Header.Get(natsgo.MsgIdHdr) != a.String() {
		t.Fatalf("got %s, want %s", m.Header.Get(natsgo.MsgIdHdr), a)
	}
	// The relay's first pass runs after LISTEN, so from here on rows can
	// only reach it through a notification.
	for i := range 3 {
		b := insert(t, pool, fmt.Sprintf("message.n%d", i))
		if m := receive(t, msgs, 5*time.Second); m.Header.Get(natsgo.MsgIdHdr) != b.String() {
			t.Fatalf("got %s, want %s", m.Header.Get(natsgo.MsgIdHdr), b)
		}
	}
}

func TestRunRetriesAfterPublishFailure(t *testing.T) {
	pool, srv := dbtest.New(t), ensured(t)
	pub := &flakyPublisher{next: srv.Client, attempts: make(chan string, 8)}
	pub.failures.Store(2)
	msgs := subscribe(t, srv)
	runRelay(t, outbox.NewRelay(pool, pub, outbox.Config{PollInterval: time.Hour, MinBackoff: 10 * time.Millisecond}))

	id := insert(t, pool, "message.created")
	if m := receive(t, msgs, 10*time.Second); m.Header.Get(natsgo.MsgIdHdr) != id.String() {
		t.Fatalf("got %s, want %s", m.Header.Get(natsgo.MsgIdHdr), id)
	}
	for range 3 {
		if got := <-pub.attempts; got != id.String() {
			t.Errorf("attempt for %s, want %s", got, id)
		}
	}
}

var errAbort = errors.New("abort")

// The write pattern every event-emitting change follows (ADR-004): the
// resource and its outbox row commit or roll back together.
func TestResourceAndOutboxRowShareOneTransaction(t *testing.T) {
	pool := dbtest.New(t)
	write := func(fail bool) error {
		return db.WithTx(t.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			var ws uuid.UUID
			if err := tx.QueryRow(ctx,
				`INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
				"acme-"+uuid.NewString()[:8], uuid.New()).Scan(&ws); err != nil {
				return err
			}
			id := uuid.New()
			if _, err := tx.Exec(ctx,
				`INSERT INTO outbox_events (id, event_type, aggregate_id, workspace_id, payload) VALUES ($1, 'workspace.created', $2, $2, $3)`,
				id, ws, envelope(id, "workspace.created")); err != nil {
				return err
			}
			if fail {
				return errAbort
			}
			return nil
		})
	}
	count := func() (workspaces, events int) {
		if err := pool.QueryRow(t.Context(),
			`SELECT (SELECT count(*) FROM workspaces), (SELECT count(*) FROM outbox_events)`).Scan(&workspaces, &events); err != nil {
			t.Fatal(err)
		}
		return
	}

	if err := write(true); !errors.Is(err, errAbort) {
		t.Fatalf("err = %v, want errAbort", err)
	}
	if w, e := count(); w != 0 || e != 0 {
		t.Errorf("after rollback: %d workspaces, %d events; want 0, 0", w, e)
	}
	if err := write(false); err != nil {
		t.Fatal(err)
	}
	if w, e := count(); w != 1 || e != 1 {
		t.Errorf("after commit: %d workspaces, %d events; want 1, 1", w, e)
	}
}
