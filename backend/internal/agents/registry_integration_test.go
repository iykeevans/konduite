//go:build integration

package agents_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/packages/contracts"
)

type registryEnv struct {
	pool  *pgxpool.Pool
	reg   *agents.Registry
	sync  *agents.Service
	rt    agents.Runtime
	owner uuid.UUID
}

func newRegistryEnv(t *testing.T) registryEnv {
	t.Helper()
	pool := dbtest.New(t)
	e := registryEnv{
		pool: pool,
		reg:  agents.NewRegistry(agents.NewPostgresRegistryRepository(pool), authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))),
		sync: agents.NewService(agents.NewPostgresRepository(pool)),
	}
	e.rt, e.owner = memberRuntime(t, pool)
	return e
}

// memberRuntime creates a workspace with a runtime whose owner is a developer
// there.
func memberRuntime(t *testing.T, pool *pgxpool.Pool) (agents.Runtime, uuid.UUID) {
	t.Helper()
	rt := newRuntime(t, pool)
	var owner uuid.UUID
	if err := pool.QueryRow(t.Context(), `SELECT owner_id FROM agent_runtime_instances WHERE id = $1`, rt.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'developer')`,
		rt.WorkspaceID, owner); err != nil {
		t.Fatal(err)
	}
	return rt, owner
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPreRegisteredAgentIsTakenOverByTheNextHello(t *testing.T) {
	e := newRegistryEnv(t)
	ctx := t.Context()

	registered, err := e.reg.Register(ctx, user(e.owner), e.rt.ID, agents.RegisterInput{
		Provider: "claude_code", Name: "claude", DisplayName: "Reviewer", Description: "reviews PRs",
		Capabilities: contracts.AgentCapabilities{SupportsCancel: ptr(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Status != agents.StatusOffline || registered.DisplayName != "Reviewer" || registered.Description != "reviews PRs" {
		t.Errorf("registered = %+v, want an offline agent with its labels", registered)
	}

	announced, err := e.sync.SyncFromHello(ctx, e.rt, hello(contracts.AnnouncedAgent{
		Provider: "claude_code", Name: "claude", Version: ptr("2.1.0"),
		Capabilities: &contracts.AgentCapabilities{SupportsCancel: ptr(true)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if announced[0].ID != registered.ID {
		t.Fatalf("hello created agent %s, want it to reuse the registered %s", announced[0].ID, registered.ID)
	}

	got, err := e.reg.Get(ctx, user(e.owner), registered.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != agents.StatusOnline || got.Version != "2.1.0" || !*got.Capabilities.SupportsCancel {
		t.Errorf("after hello = %+v, want online with the announced version and capabilities", got)
	}
	if got.DisplayName != "Reviewer" || got.Description != "reviews PRs" {
		t.Errorf("hello lost the labels: %+v", got)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM agents`); n != 1 {
		t.Errorf("%d agent rows, want 1", n)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM audit_logs WHERE action = 'agent.created' AND resource_id = $1 AND actor_id = $2`,
		registered.ID, e.owner); n != 1 {
		t.Errorf("%d agent.created audit entries, want 1", n)
	}
}

func TestRegisteringAnAgentTwiceIsAConflict(t *testing.T) {
	e := newRegistryEnv(t)
	if _, err := e.sync.SyncFromHello(t.Context(), e.rt, hello(contracts.AnnouncedAgent{Provider: "codex", Name: "codex"})); err != nil {
		t.Fatal(err)
	}
	_, err := e.reg.Register(t.Context(), user(e.owner), e.rt.ID, agents.RegisterInput{Provider: "codex", Name: "codex"})
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindConflict {
		t.Errorf("err = %v, want conflict", err)
	}
	_, err = e.reg.RegisterForRuntime(t.Context(), e.rt, agents.RegisterInput{Provider: "codex", Name: "codex"})
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindConflict {
		t.Errorf("runtime registration err = %v, want conflict", err)
	}
}

func TestRegisteringOnARuntimeInAnotherWorkspaceIsRejected(t *testing.T) {
	e := newRegistryEnv(t)
	foreign, _ := memberRuntime(t, e.pool)

	_, err := e.reg.Register(t.Context(), user(e.owner), foreign.ID, agents.RegisterInput{Provider: "echo", Name: "echo"})
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindNotFound {
		t.Errorf("err = %v, want not found", err)
	}
	_, err = e.reg.RegisterForRuntime(t.Context(), agents.Runtime{ID: foreign.ID, WorkspaceID: e.rt.WorkspaceID},
		agents.RegisterInput{Provider: "echo", Name: "echo"})
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindUnauthenticated {
		t.Errorf("runtime registration err = %v, want unauthenticated", err)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM agents`); n != 0 {
		t.Errorf("%d agents stored, want 0", n)
	}
}

func TestDeletingAnAgentRemovesItsGrants(t *testing.T) {
	e := newRegistryEnv(t)
	ctx := t.Context()
	a, err := e.reg.RegisterForRuntime(ctx, e.rt, agents.RegisterInput{Provider: "echo", Name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	var project uuid.UUID
	if err := e.pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, 'api', $2) RETURNING id`,
		e.rt.WorkspaceID, e.owner).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO agent_project_access (agent_id, project_id, workspace_id, granted_by) VALUES ($1, $2, $3, $4)`,
		a.ID, project, e.rt.WorkspaceID, e.owner); err != nil {
		t.Fatal(err)
	}

	if err := e.reg.Delete(ctx, user(e.owner), a.ID); err != nil {
		t.Fatal(err)
	}

	if n := count(t, e.pool, `SELECT (SELECT count(*) FROM agents) + (SELECT count(*) FROM agent_project_access)`); n != 0 {
		t.Errorf("%d agent and grant rows left, want 0", n)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM audit_logs WHERE action = 'agent.deleted' AND resource_id = $1`, a.ID); n != 1 {
		t.Errorf("%d agent.deleted audit entries, want 1", n)
	}
	_, err = e.reg.Get(ctx, user(e.owner), a.ID)
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindNotFound {
		t.Errorf("get after delete: err = %v, want not found", err)
	}
}

func TestUpdateListAndListForRuntime(t *testing.T) {
	e := newRegistryEnv(t)
	ctx := t.Context()
	other, _ := memberRuntime(t, e.pool)
	announced, err := e.sync.SyncFromHello(ctx, e.rt, hello(
		contracts.AnnouncedAgent{Provider: "claude_code", Name: "claude"},
		contracts.AnnouncedAgent{Provider: "echo", Name: "echo"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.sync.SyncFromHello(ctx, other, hello(contracts.AnnouncedAgent{Provider: "echo", Name: "echo"})); err != nil {
		t.Fatal(err)
	}

	name := "Reviewer"
	updated, err := e.reg.Update(ctx, user(e.owner), announced[0].ID, agents.UpdateInput{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Reviewer" || updated.Status != agents.StatusOnline {
		t.Errorf("updated = %+v", updated)
	}
	if _, err := e.reg.Update(ctx, user(e.owner), announced[0].ID, agents.UpdateInput{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.pool, `SELECT count(*) FROM audit_logs WHERE action = 'agent.updated'`); n != 1 {
		t.Errorf("%d agent.updated entries, want 1: a no-op update records nothing", n)
	}

	var seen []uuid.UUID
	in := agents.ListInput{WorkspaceID: e.rt.WorkspaceID, Limit: 1}
	for {
		page, err := e.reg.List(ctx, user(e.owner), in)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Items {
			seen = append(seen, a.ID)
		}
		if page.Next == nil {
			break
		}
		in.After = page.Next
	}
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Errorf("listed %v, want this workspace's 2 agents", seen)
	}

	page, err := e.reg.List(ctx, user(e.owner), agents.ListInput{WorkspaceID: e.rt.WorkspaceID, RuntimeID: other.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Errorf("filtering by another workspace's runtime listed %v", page.Items)
	}

	mine, err := e.reg.ListForRuntime(ctx, e.rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 || mine[0].Name != "claude" || mine[1].Name != "echo" {
		t.Errorf("runtime agents = %+v", mine)
	}
}
