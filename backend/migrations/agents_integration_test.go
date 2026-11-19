//go:build integration

package migrations_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

func newAgent(t *testing.T, pool *pgxpool.Pool, workspaceID, runtimeID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'claude_code', 'claude') RETURNING id`,
		workspaceID, runtimeID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func newProject(t *testing.T, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, 'api', $2) RETURNING id`,
		workspaceID, alice).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

const grantAccess = `INSERT INTO agent_project_access (agent_id, project_id, workspace_id, granted_by) VALUES ($1, $2, $3, $4)`

func TestRuntimeAnnouncesAnAgentOnce(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	rt := newRuntime(t, pool, ws)
	newAgent(t, pool, ws, rt)
	err := exec(t, pool, `INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'claude_code', 'claude')`, ws, rt)
	wantCode(t, err, uniqueViolation)
}

func TestAgentMustBeInItsRuntimesWorkspace(t *testing.T) {
	pool := dbtest.New(t)
	rt := newRuntime(t, pool, newWorkspace(t, pool, "acme"))
	other := newWorkspace(t, pool, "other")
	err := exec(t, pool, `INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'codex', 'codex')`, other, rt)
	wantCode(t, err, foreignKeyViolation)
}

func TestAgentColumnsAreChecked(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	rt := newRuntime(t, pool, ws)
	for name, sql := range map[string]string{
		"bad provider":               `INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'Claude Code', 'a')`,
		"empty name":                 `INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'codex', '')`,
		"bad status":                 `INSERT INTO agents (workspace_id, runtime_id, provider, name, status) VALUES ($1, $2, 'codex', 'a', 'asleep')`,
		"capabilities not an object": `INSERT INTO agents (workspace_id, runtime_id, provider, name, capabilities) VALUES ($1, $2, 'codex', 'a', '[]')`,
	} {
		t.Run(name, func(t *testing.T) {
			wantCode(t, exec(t, pool, sql, ws, rt), checkViolation)
		})
	}
}

func TestProjectAccessStaysInsideTheWorkspace(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	agent := newAgent(t, pool, ws, newRuntime(t, pool, ws))
	other := newWorkspace(t, pool, "other")
	foreignProject := newProject(t, pool, other)

	wantCode(t, exec(t, pool, grantAccess, agent, foreignProject, ws, alice), foreignKeyViolation)
	wantCode(t, exec(t, pool, grantAccess, agent, foreignProject, other, alice), foreignKeyViolation)
}

func TestAgentGetsOneGrantPerProject(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	agent := newAgent(t, pool, ws, newRuntime(t, pool, ws))
	project := newProject(t, pool, ws)
	mustExec(t, pool, grantAccess, agent, project, ws, alice)
	wantCode(t, exec(t, pool, grantAccess, agent, project, ws, alice), uniqueViolation)
}

func TestDeletingARuntimeRemovesItsAgentsAndTheirGrants(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	rt := newRuntime(t, pool, ws)
	agent := newAgent(t, pool, ws, rt)
	mustExec(t, pool, grantAccess, agent, newProject(t, pool, ws), ws, alice)

	mustExec(t, pool, `DELETE FROM agent_runtime_instances WHERE id = $1`, rt)

	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT (SELECT count(*) FROM agents) + (SELECT count(*) FROM agent_project_access)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d agent rows left after deleting the runtime, want 0", n)
	}
}

func TestDeletingAProjectRemovesItsGrants(t *testing.T) {
	pool := dbtest.New(t)
	ws := newWorkspace(t, pool, "acme")
	agent := newAgent(t, pool, ws, newRuntime(t, pool, ws))
	project := newProject(t, pool, ws)
	mustExec(t, pool, grantAccess, agent, project, ws, alice)

	mustExec(t, pool, `DELETE FROM projects WHERE id = $1`, project)

	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_project_access`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d grants left after deleting the project, want 0", n)
	}
}
