//go:build integration

package migrations_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

const foreignKeyViolation = "23503"

func newRuntime(t *testing.T, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO agent_runtime_instances (workspace_id, owner_id, name) VALUES ($1, $2, 'laptop') RETURNING id`,
		workspaceID, alice).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRuntimeNeedsAName(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	err := exec(t, pool, `INSERT INTO agent_runtime_instances (workspace_id, owner_id, name) VALUES ($1, $2, '  ')`, ws, alice)
	wantCode(t, err, checkViolation)
}

func TestRuntimeHasOneCredential(t *testing.T) {
	pool := dbtest.New(t)
	rt := newRuntime(t, pool, newWorkspace(t, pool, "acme"))
	mustExec(t, pool, `INSERT INTO runtime_credentials (runtime_id, secret_hash) VALUES ($1, $2)`, rt, make([]byte, 32))
	err := exec(t, pool, `INSERT INTO runtime_credentials (runtime_id, secret_hash) VALUES ($1, $2)`, rt, []byte("another hash, 32 bytes long....."))
	wantCode(t, err, uniqueViolation)
}

func TestCredentialHashMustBeSHA256Sized(t *testing.T) {
	pool := dbtest.New(t)
	rt := newRuntime(t, pool, newWorkspace(t, pool, "acme"))
	err := exec(t, pool, `INSERT INTO runtime_credentials (runtime_id, secret_hash) VALUES ($1, $2)`, rt, []byte("short"))
	wantCode(t, err, checkViolation)
}

func TestCredentialNeedsAnExistingRuntime(t *testing.T) {
	pool := dbtest.New(t)
	err := exec(t, pool, `INSERT INTO runtime_credentials (runtime_id, secret_hash) VALUES (gen_random_uuid(), $1)`, make([]byte, 32))
	wantCode(t, err, foreignKeyViolation)
}

func TestDeletingAWorkspaceRemovesItsRuntimesAndCredentials(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	rt := newRuntime(t, pool, ws)
	mustExec(t, pool, `INSERT INTO runtime_credentials (runtime_id, secret_hash) VALUES ($1, $2)`, rt, make([]byte, 32))

	mustExec(t, pool, `DELETE FROM workspaces WHERE id = $1`, ws)

	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT (SELECT count(*) FROM agent_runtime_instances) + (SELECT count(*) FROM runtime_credentials)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d runtime rows left after deleting the workspace, want 0", n)
	}
}
