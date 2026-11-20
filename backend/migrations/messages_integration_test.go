//go:build integration

package migrations_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

const insertMessage = `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, message_type, content)
VALUES ($1, $2, 'user', $3, $4, 'text', 'hi') RETURNING id`

func newMessage(t *testing.T, pool *pgxpool.Pool, conversationID string, seq int64, key string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), insertMessage, conversationID, seq, alice, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMessageSequenceIsUniquePerConversation(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	c1, c2 := newConversation(t, pool, ws), newConversation(t, pool, ws)
	newMessage(t, pool, c1, 1, "a")
	newMessage(t, pool, c2, 1, "a")
	wantCode(t, exec(t, pool, insertMessage, c1, 1, alice, "b"), uniqueViolation)
	wantCode(t, exec(t, pool, insertMessage, c1, 0, alice, "c"), checkViolation)
}

func TestMessageIdempotencyKeyIsUniquePerSenderInAConversation(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	c1, c2 := newConversation(t, pool, ws), newConversation(t, pool, ws)
	newMessage(t, pool, c1, 1, "k")
	wantCode(t, exec(t, pool, insertMessage, c1, 2, alice, "k"), uniqueViolation)
	mustExec(t, pool, insertMessage, c1, 2, bob, "k")
	mustExec(t, pool, insertMessage, c2, 1, alice, "k")
	wantCode(t, exec(t, pool, insertMessage, c1, 3, alice, ""), checkViolation)
}

func TestMessageColumnsAreChecked(t *testing.T) {
	pool := dbtest.New(t)
	c := newConversation(t, pool, newWorkspace(t, pool, "acme"))
	cases := []struct {
		name, sql string
	}{
		{"unknown sender type", `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content) VALUES ($1, 1, 'slack', gen_random_uuid(), 'k', 'hi')`},
		{"unknown message type", `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, message_type, content) VALUES ($1, 1, 'user', gen_random_uuid(), 'k', 'poll', 'hi')`},
		{"metadata not an object", `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content, metadata) VALUES ($1, 1, 'user', gen_random_uuid(), 'k', 'hi', '[]')`},
		{"idempotency key too long", `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content) VALUES ($1, 1, 'user', gen_random_uuid(), repeat('k', 256), 'hi')`},
		{"content too long", `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content) VALUES ($1, 1, 'user', gen_random_uuid(), 'k', repeat('x', 65537))`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCode(t, exec(t, pool, tc.sql, c), checkViolation)
		})
	}

	var messageType, metadata string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content)
		 VALUES ($1, 1, 'agent', gen_random_uuid(), 'k', 'hi') RETURNING message_type, metadata::text`, c).Scan(&messageType, &metadata); err != nil {
		t.Fatal(err)
	}
	if messageType != "text" || metadata != "{}" {
		t.Errorf("defaults: message_type = %q, metadata = %s, want text and {}", messageType, metadata)
	}
}

func TestMessageParentMustBeInTheSameConversation(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	c1, c2 := newConversation(t, pool, ws), newConversation(t, pool, ws)
	parent := newMessage(t, pool, c1, 1, "p")
	reply := `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content, parent_id)
		VALUES ($1, $2, 'user', $3, $4, 'reply', $5)`
	mustExec(t, pool, reply, c1, 2, bob, "r", parent)
	wantCode(t, exec(t, pool, reply, c2, 1, bob, "r", parent), foreignKeyViolation)
	wantCode(t, exec(t, pool, reply, c1, 3, bob, "r2", "00000000-0000-0000-0000-000000000001"), foreignKeyViolation)
}

func TestDeletingAConversationRemovesItsMessages(t *testing.T) {
	pool := dbtest.New(t)
	c := newConversation(t, pool, newWorkspace(t, pool, "acme"))
	parent := newMessage(t, pool, c, 1, "p")
	mustExec(t, pool, `INSERT INTO messages (conversation_id, sequence_number, sender_type, sender_id, idempotency_key, content, parent_id)
		VALUES ($1, 2, 'user', $2, 'r', 'reply', $3)`, c, bob, parent)
	mustExec(t, pool, `DELETE FROM conversations WHERE id = $1`, c)
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d messages left, want 0", n)
	}
}
