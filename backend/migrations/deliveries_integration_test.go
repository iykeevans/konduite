//go:build integration

package migrations_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

const insertDelivery = `INSERT INTO message_deliveries (workspace_id, message_id, conversation_id, sequence_number, recipient_type, recipient_id, runtime_id)
VALUES ($1, $2, $3, 1, $4, $5, $6)`

func TestMessageDeliveryIsUniquePerMessageAndRecipient(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	c := newConversation(t, pool, ws)
	m := newMessage(t, pool, c, 1, "k")
	rt := newRuntime(t, pool, ws)
	agent := newAgent(t, pool, ws, rt)

	mustExec(t, pool, insertDelivery, ws, m, c, "agent", agent, rt)
	wantCode(t, exec(t, pool, insertDelivery, ws, m, c, "agent", agent, rt), uniqueViolation)
	mustExec(t, pool, insertDelivery, ws, m, c, "user", bob, nil)
	wantCode(t, exec(t, pool, insertDelivery, ws, m, c, "agent", uuid.NewString(), nil), checkViolation)
	wantCode(t, exec(t, pool, insertDelivery, ws, m, c, "slack", uuid.NewString(), nil), checkViolation)
}

func TestMessageDeliveryStaysInItsMessagesConversationAndWorkspace(t *testing.T) {
	pool := dbtest.New(t)
	ws, other := newWorkspace(t, pool, "acme"), newWorkspace(t, pool, "globex")
	c1, c2 := newConversation(t, pool, ws), newConversation(t, pool, ws)
	m := newMessage(t, pool, c1, 1, "k")
	foreignRT := newRuntime(t, pool, other)

	wantCode(t, exec(t, pool, insertDelivery, ws, m, c2, "user", bob, nil), foreignKeyViolation)
	wantCode(t, exec(t, pool, insertDelivery, ws, m, c1, "runtime", foreignRT, foreignRT), foreignKeyViolation)
}

func TestMessageDeliveriesGoWithTheirMessage(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	c := newConversation(t, pool, ws)
	m := newMessage(t, pool, c, 1, "k")
	mustExec(t, pool, insertDelivery, ws, m, c, "user", bob, nil)

	mustExec(t, pool, `DELETE FROM conversations WHERE id = $1`, c)
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM message_deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d deliveries left after their conversation was deleted, want 0", n)
	}
}

func TestOutboxSubjectMustBeUnderAgenthub(t *testing.T) {
	pool := dbtest.New(t)
	const insert = `INSERT INTO outbox_events (event_type, aggregate_id, payload, subject) VALUES ('runtime.deliver', gen_random_uuid(), '{}', $1)`
	mustExec(t, pool, insert, "agenthub.rt.x.deliver")
	wantCode(t, exec(t, pool, insert, "dlq.agenthub.rt.x"), checkViolation)
}
