//go:build integration

package migrations_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

func newConversation(t *testing.T, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO conversations (workspace_id, created_by) VALUES ($1, $2) RETURNING id`,
		workspaceID, alice).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConversationStartsAtSequenceZero(t *testing.T) {
	pool := dbtest.New(t)
	c := newConversation(t, pool, newWorkspace(t, pool, "acme"))
	var lastSeq int64
	var title string
	if err := pool.QueryRow(t.Context(), `SELECT last_seq, title FROM conversations WHERE id = $1`, c).Scan(&lastSeq, &title); err != nil {
		t.Fatal(err)
	}
	if lastSeq != 0 || title != "" {
		t.Errorf("last_seq = %d, title = %q, want 0 and empty", lastSeq, title)
	}
	wantCode(t, exec(t, pool, `UPDATE conversations SET last_seq = -1 WHERE id = $1`, c), checkViolation)
}

func TestConversationNeedsAnExistingProject(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	err := exec(t, pool, `INSERT INTO conversations (workspace_id, project_id, created_by) VALUES ($1, gen_random_uuid(), $2)`, ws, alice)
	wantCode(t, err, foreignKeyViolation)
}

func TestParticipantKindMustBeKnown(t *testing.T) {
	pool := dbtest.New(t)
	c := newConversation(t, pool, newWorkspace(t, pool, "acme"))
	err := exec(t, pool, `INSERT INTO conversation_participants (conversation_id, participant_type, participant_id) VALUES ($1, 'slack', $2)`, c, bob)
	wantCode(t, err, checkViolation)
}

func TestParticipantJoinsAConversationOnce(t *testing.T) {
	pool := dbtest.New(t)
	c := newConversation(t, pool, newWorkspace(t, pool, "acme"))
	insert := `INSERT INTO conversation_participants (conversation_id, participant_type, participant_id) VALUES ($1, $2, $3)`
	mustExec(t, pool, insert, c, "user", bob)
	wantCode(t, exec(t, pool, insert, c, "user", bob), uniqueViolation)
	mustExec(t, pool, insert, c, "agent", bob)
}

func TestDeletingAWorkspaceRemovesItsConversationsAndParticipants(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	c := newConversation(t, pool, ws)
	mustExec(t, pool, `INSERT INTO conversation_participants (conversation_id, participant_type, participant_id) VALUES ($1, 'user', $2)`, c, alice)

	mustExec(t, pool, `DELETE FROM workspaces WHERE id = $1`, ws)

	var conversations, participants int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM conversations`).Scan(&conversations)
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM conversation_participants`).Scan(&participants)
	if conversations != 0 || participants != 0 {
		t.Errorf("after delete: conversations=%d participants=%d, want 0, 0", conversations, participants)
	}
}
