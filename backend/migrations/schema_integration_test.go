//go:build integration

package migrations_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

const (
	uniqueViolation = "23505"
	checkViolation  = "23514"
)

const (
	alice = "00000000-0000-0000-0000-00000000000a"
	bob   = "00000000-0000-0000-0000-00000000000b"
)

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) error {
	t.Helper()
	_, err := pool.Exec(t.Context(), sql, args...)
	return err
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if err := exec(t, pool, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("err = %v, want SQLSTATE %s", err, code)
	}
}

func newWorkspace(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO workspaces (name, slug, created_by) VALUES ($1, $1, $2) RETURNING id`, slug, alice).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWorkspaceSlugIsUnique(t *testing.T) {
	pool := dbtest.New(t)
	newWorkspace(t, pool, "acme")
	err := exec(t, pool, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme 2', 'acme', $1)`, bob)
	wantCode(t, err, uniqueViolation)
}

func TestMemberRoleMustBeKnown(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	err := exec(t, pool, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'superuser')`, ws, bob)
	wantCode(t, err, checkViolation)
}

func TestUserJoinsAWorkspaceOnce(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	mustExec(t, pool, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'developer')`, ws, bob)
	err := exec(t, pool, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, ws, bob)
	wantCode(t, err, uniqueViolation)
}

func TestDeletingAWorkspaceRemovesItsMembersAndProjects(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	other := newWorkspace(t, pool, "other")
	mustExec(t, pool, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, ws, alice)
	mustExec(t, pool, `INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, 'api', $2)`, ws, alice)
	mustExec(t, pool, `INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, 'api', $2)`, other, alice)

	mustExec(t, pool, `DELETE FROM workspaces WHERE id = $1`, ws)

	var members, projects, otherProjects int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM workspace_members WHERE workspace_id = $1`, ws).Scan(&members)
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM projects WHERE workspace_id = $1`, ws).Scan(&projects)
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM projects WHERE workspace_id = $1`, other).Scan(&otherProjects)
	if members != 0 || projects != 0 || otherProjects != 1 {
		t.Errorf("after delete: members=%d projects=%d otherProjects=%d, want 0, 0, 1", members, projects, otherProjects)
	}
}

func TestInboundEventIsRecordedOncePerProvider(t *testing.T) {
	pool := dbtest.New(t)
	insert := `INSERT INTO inbound_events (provider, external_event_id, payload) VALUES ($1, 'Ev123', '{}')`
	mustExec(t, pool, insert, "slack")
	wantCode(t, exec(t, pool, insert, "slack"), uniqueViolation)
	mustExec(t, pool, insert, "github") // same id from another provider is a different event
}
