//go:build integration

package messages_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/messages"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

func seedConversation(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var ws, id uuid.UUID
	if err := pool.QueryRow(t.Context(), `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
		"acme-"+uuid.NewString()[:8], uuid.New()).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO conversations (workspace_id, created_by) VALUES ($1, $2) RETURNING id`,
		ws, uuid.New()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func lastSeq(t *testing.T, pool *pgxpool.Pool, conversation uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(t.Context(), `SELECT last_seq FROM conversations WHERE id = $1`, conversation).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func message(conversation uuid.UUID, sender uuid.UUID, key string) messages.NewMessage {
	return messages.NewMessage{
		ConversationID: conversation,
		Sender:         messages.Sender{Kind: conversations.KindUser, ID: sender},
		IdempotencyKey: key,
		Type:           messages.TypeText,
		Content:        "hello " + key,
	}
}

func TestAppendNumbersMessagesFromOne(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	c := seedConversation(t, pool)
	repo := messages.NewPostgresRepository(pool)
	alice := uuid.New()

	first, created, err := repo.Append(ctx, message(c, alice, "a"))
	if err != nil || !created {
		t.Fatalf("Append = created %v, err %v", created, err)
	}
	n := message(c, alice, "b")
	n.ParentID = &first.ID
	n.Type = messages.TypeTask
	n.Metadata = json.RawMessage(`{"priority": "high"}`)
	second, _, err := repo.Append(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if first.SequenceNumber != 1 || second.SequenceNumber != 2 || lastSeq(t, pool, c) != 2 {
		t.Errorf("sequence numbers %d, %d, last_seq %d; want 1, 2, 2", first.SequenceNumber, second.SequenceNumber, lastSeq(t, pool, c))
	}
	if string(first.Metadata) != "{}" {
		t.Errorf("default metadata = %s, want {}", first.Metadata)
	}

	got, err := repo.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != c || got.Sender.Kind != conversations.KindUser || got.Sender.ID != alice ||
		got.IdempotencyKey != "b" || got.ParentID == nil || *got.ParentID != first.ID || got.Type != messages.TypeTask ||
		got.Content != "hello b" || got.CreatedAt.IsZero() {
		t.Errorf("Get = %+v", got)
	}
	var meta map[string]string
	if err := json.Unmarshal(got.Metadata, &meta); err != nil || meta["priority"] != "high" {
		t.Errorf("metadata = %s (%v)", got.Metadata, err)
	}

	if _, err := repo.Get(ctx, uuid.New()); !errors.Is(err, messages.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestAppendReturnsTheOriginalOnARetryWithoutUsingASequenceNumber(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	c := seedConversation(t, pool)
	repo := messages.NewPostgresRepository(pool)
	alice, bob := uuid.New(), uuid.New()

	original, _, err := repo.Append(ctx, message(c, alice, "k"))
	if err != nil {
		t.Fatal(err)
	}
	retry := message(c, alice, "k")
	retry.Content = "a different body"
	again, created, err := repo.Append(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != original.ID || again.SequenceNumber != 1 || again.Content != original.Content {
		t.Errorf("retry = %+v (created %v), want the original %+v", again, created, original)
	}
	if got := lastSeq(t, pool, c); got != 1 {
		t.Errorf("last_seq = %d after a retry, want 1", got)
	}

	other, created, err := repo.Append(ctx, message(c, bob, "k"))
	if err != nil || !created || other.SequenceNumber != 2 {
		t.Errorf("same key from another sender = seq %d, created %v, err %v; want a new message 2", other.SequenceNumber, created, err)
	}
}

func TestAppendRejectsUnknownConversationsAndForeignParents(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	c1, c2 := seedConversation(t, pool), seedConversation(t, pool)
	repo := messages.NewPostgresRepository(pool)
	alice := uuid.New()

	if _, _, err := repo.Append(ctx, message(uuid.New(), alice, "a")); !errors.Is(err, messages.ErrConversationNotFound) {
		t.Errorf("unknown conversation: err = %v, want ErrConversationNotFound", err)
	}
	foreign, _, err := repo.Append(ctx, message(c2, alice, "a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []uuid.UUID{foreign.ID, uuid.New()} {
		n := message(c1, alice, "reply-"+parent.String())
		n.ParentID = &parent
		if _, _, err := repo.Append(ctx, n); !errors.Is(err, messages.ErrParentNotFound) {
			t.Errorf("parent %v: err = %v, want ErrParentNotFound", parent, err)
		}
	}
	if got := lastSeq(t, pool, c1); got != 0 {
		t.Errorf("last_seq = %d after rejected writes, want 0", got)
	}
}

func TestAppendKeepsSequenceNumbersGaplessUnderConcurrentWriters(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	c := seedConversation(t, pool)
	repo := messages.NewPostgresRepository(pool)

	const writers, perWriter = 8, 10
	var mu sync.Mutex
	var seqs []int64
	var wg sync.WaitGroup
	errc := make(chan error, writers)
	for w := range writers {
		sender := uuid.New()
		wg.Go(func() {
			for i := range perWriter {
				m, _, err := repo.Append(ctx, message(c, sender, fmt.Sprintf("w%d-%d", w, i)))
				if err != nil {
					errc <- err
					return
				}
				mu.Lock()
				seqs = append(seqs, m.SequenceNumber)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
	slices.Sort(seqs)
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("sequence numbers %v are not 1..%d without gaps", seqs, writers*perWriter)
		}
	}
	if got := lastSeq(t, pool, c); got != writers*perWriter {
		t.Errorf("last_seq = %d, want %d", got, writers*perWriter)
	}
}

func TestConcurrentRetriesCreateOneMessage(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	c := seedConversation(t, pool)
	repo := messages.NewPostgresRepository(pool)
	alice := uuid.New()

	const attempts = 10
	results := make([]messages.Message, attempts)
	createdCount := make([]bool, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range attempts {
		wg.Go(func() {
			<-start
			results[i], createdCount[i], errs[i] = repo.Append(ctx, message(c, alice, "same"))
		})
	}
	close(start)
	wg.Wait()

	created := 0
	for i := range attempts {
		if errs[i] != nil {
			t.Fatalf("attempt %d: %v", i, errs[i])
		}
		if results[i].ID != results[0].ID || results[i].SequenceNumber != 1 {
			t.Errorf("attempt %d got %v seq %d, want the one message", i, results[i].ID, results[i].SequenceNumber)
		}
		if createdCount[i] {
			created++
		}
	}
	if created != 1 {
		t.Errorf("%d attempts reported created, want 1", created)
	}
	if got := lastSeq(t, pool, c); got != 1 {
		t.Errorf("last_seq = %d, want 1", got)
	}
}

func TestAppendTxRunsInTheCallersTransaction(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	c := seedConversation(t, pool)
	alice := uuid.New()
	errAbort := errors.New("abort")

	err := db.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := messages.AppendTx(ctx, tx, message(c, alice, "a")); err != nil {
			return err
		}
		return errAbort
	})
	if !errors.Is(err, errAbort) {
		t.Fatalf("err = %v", err)
	}
	if got := lastSeq(t, pool, c); got != 0 {
		t.Errorf("last_seq = %d after rollback, want 0", got)
	}

	original, _, err := messages.NewPostgresRepository(pool).Append(ctx, message(c, alice, "b"))
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		m, created, err := messages.AppendTx(ctx, tx, message(c, alice, "b"))
		if err != nil {
			return err
		}
		if created || m.ID != original.ID {
			t.Errorf("retry in caller tx = %v created %v, want the original", m.ID, created)
		}
		fresh, created, err := messages.AppendTx(ctx, tx, message(c, alice, "c"))
		if err != nil {
			return err
		}
		if !created || fresh.SequenceNumber != 2 {
			t.Errorf("new message after a retry in the same tx = seq %d created %v, want 2", fresh.SequenceNumber, created)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
