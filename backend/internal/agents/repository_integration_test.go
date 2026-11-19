//go:build integration

package agents_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/packages/contracts"
)

func newRuntime(t *testing.T, pool *pgxpool.Pool) agents.Runtime {
	t.Helper()
	ctx := t.Context()
	owner := uuid.New()
	rt := agents.Runtime{}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
		"acme-"+uuid.NewString()[:8], owner).Scan(&rt.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime_instances (workspace_id, owner_id, name) VALUES ($1, $2, 'laptop') RETURNING id`,
		rt.WorkspaceID, owner).Scan(&rt.ID); err != nil {
		t.Fatal(err)
	}
	return rt
}

func hello(as ...contracts.AnnouncedAgent) contracts.RuntimeHello {
	return contracts.RuntimeHello{RuntimeVersion: "0.1.0", Agents: as}
}

func statuses(t *testing.T, pool *pgxpool.Pool, rt agents.Runtime) map[string]agents.Status {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT name, status FROM agents WHERE runtime_id = $1`, rt.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]agents.Status{}
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			t.Fatal(err)
		}
		got[name] = agents.Status(status)
	}
	return got
}

func TestSyncFromHelloStoresAnnouncedAgents(t *testing.T) {
	pool := dbtest.New(t)
	svc := agents.NewService(agents.NewPostgresRepository(pool))
	rt := newRuntime(t, pool)
	caps := contracts.AgentCapabilities{SupportsCancel: ptr(true), SessionScope: ptr("project")}

	got, err := svc.SyncFromHello(t.Context(), rt, hello(
		contracts.AnnouncedAgent{Provider: "claude_code", Name: "claude", Version: ptr("2.1.0"), Capabilities: &caps},
		contracts.AnnouncedAgent{Provider: "echo", Name: "echo"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	a := got[0]
	if a.ID == uuid.Nil || a.WorkspaceID != rt.WorkspaceID || a.RuntimeID != rt.ID || a.Provider != "claude_code" ||
		a.Name != "claude" || a.Version != "2.1.0" || a.Status != agents.StatusOnline || a.LastSeenAt.IsZero() {
		t.Errorf("agent = %+v", a)
	}
	if !reflect.DeepEqual(a.Capabilities, caps) {
		t.Errorf("capabilities = %+v, want %+v", a.Capabilities, caps)
	}
	if got[1].Name != "echo" || got[1].Version != "" || !reflect.DeepEqual(got[1].Capabilities, contracts.AgentCapabilities{}) {
		t.Errorf("second agent = %+v", got[1])
	}
}

func TestSyncFromHelloIsIdempotent(t *testing.T) {
	pool := dbtest.New(t)
	svc := agents.NewService(agents.NewPostgresRepository(pool))
	rt := newRuntime(t, pool)
	h := hello(contracts.AnnouncedAgent{Provider: "codex", Name: "codex", Version: ptr("1")})

	first, err := svc.SyncFromHello(t.Context(), rt, h)
	if err != nil {
		t.Fatal(err)
	}
	h.Agents[0].Version = ptr("2")
	second, err := svc.SyncFromHello(t.Context(), rt, h)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID != first[0].ID {
		t.Errorf("re-announced agent got a new id: %s, was %s", second[0].ID, first[0].ID)
	}
	if second[0].Version != "2" {
		t.Errorf("version = %q, want the newly announced 2", second[0].Version)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d agent rows, want 1", n)
	}
}

func TestSyncFromHelloMarksAgentsNoLongerAnnouncedOffline(t *testing.T) {
	pool := dbtest.New(t)
	svc := agents.NewService(agents.NewPostgresRepository(pool))
	rt := newRuntime(t, pool)
	other := newRuntime(t, pool)
	claude := contracts.AnnouncedAgent{Provider: "claude_code", Name: "claude"}
	codex := contracts.AnnouncedAgent{Provider: "codex", Name: "codex"}
	ctx := t.Context()

	first, err := svc.SyncFromHello(ctx, rt, hello(claude, codex))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncFromHello(ctx, other, hello(codex)); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SyncFromHello(ctx, rt, hello(claude)); err != nil {
		t.Fatal(err)
	}
	if got, want := statuses(t, pool, rt), map[string]agents.Status{"claude": agents.StatusOnline, "codex": agents.StatusOffline}; !reflect.DeepEqual(got, want) {
		t.Errorf("after dropping codex: %v, want %v", got, want)
	}
	if got := statuses(t, pool, other)["codex"]; got != agents.StatusOnline {
		t.Errorf("another runtime's codex is %s, want online", got)
	}

	if _, err := svc.SyncFromHello(ctx, rt, hello()); err != nil {
		t.Fatal(err)
	}
	if got, want := statuses(t, pool, rt), map[string]agents.Status{"claude": agents.StatusOffline, "codex": agents.StatusOffline}; !reflect.DeepEqual(got, want) {
		t.Errorf("after an empty hello: %v, want %v", got, want)
	}

	back, err := svc.SyncFromHello(ctx, rt, hello(codex))
	if err != nil {
		t.Fatal(err)
	}
	if back[0].ID != first[1].ID || back[0].Status != agents.StatusOnline {
		t.Errorf("re-announced codex = %+v, want id %s online", back[0], first[1].ID)
	}
}

func TestSyncFromHelloForAGoneRuntimeIsNotFound(t *testing.T) {
	pool := dbtest.New(t)
	svc := agents.NewService(agents.NewPostgresRepository(pool))
	rt := newRuntime(t, pool)
	other := newRuntime(t, pool)
	h := hello(contracts.AnnouncedAgent{Provider: "echo", Name: "echo"})

	for name, r := range map[string]agents.Runtime{
		"unknown runtime":              {ID: uuid.New(), WorkspaceID: rt.WorkspaceID},
		"runtime in another workspace": {ID: rt.ID, WorkspaceID: other.WorkspaceID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.SyncFromHello(t.Context(), r, h)
			if e, ok := errs.As(err); !ok || e.Kind != errs.KindNotFound {
				t.Errorf("err = %v, want not found", err)
			}
		})
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM agents`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d agents stored for a runtime that doesn't match, want 0", n)
	}
}

func TestConcurrentHellosFromOneRuntimeAgree(t *testing.T) {
	pool := dbtest.New(t)
	svc := agents.NewService(agents.NewPostgresRepository(pool))
	rt := newRuntime(t, pool)
	h := hello(contracts.AnnouncedAgent{Provider: "claude_code", Name: "claude"}, contracts.AnnouncedAgent{Provider: "echo", Name: "echo"})

	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := svc.SyncFromHello(t.Context(), rt, h)
			errc <- err
		})
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := statuses(t, pool, rt), map[string]agents.Status{"claude": agents.StatusOnline, "echo": agents.StatusOnline}; !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
}

func TestHasProjectAccess(t *testing.T) {
	pool := dbtest.New(t)
	svc := agents.NewService(agents.NewPostgresRepository(pool))
	rt := newRuntime(t, pool)
	ctx := t.Context()
	got, err := svc.SyncFromHello(ctx, rt, hello(contracts.AnnouncedAgent{Provider: "echo", Name: "echo"}))
	if err != nil {
		t.Fatal(err)
	}
	agent := got[0].ID
	var granted, other uuid.UUID
	for _, p := range []*uuid.UUID{&granted, &other} {
		if err := pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, $2, $3) RETURNING id`,
			rt.WorkspaceID, uuid.NewString(), uuid.New()).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_project_access (agent_id, project_id, workspace_id, granted_by) VALUES ($1, $2, $3, $4)`,
		agent, granted, rt.WorkspaceID, uuid.New()); err != nil {
		t.Fatal(err)
	}

	for project, want := range map[uuid.UUID]bool{granted: true, other: false, uuid.New(): false} {
		ok, err := svc.HasProjectAccess(ctx, agent, project)
		if err != nil {
			t.Fatal(err)
		}
		if ok != want {
			t.Errorf("HasProjectAccess(%s) = %v, want %v", project, ok, want)
		}
	}
	if _, err := svc.SyncFromHello(ctx, rt, hello()); err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.HasProjectAccess(ctx, agent, granted); err != nil || !ok {
		t.Errorf("grant lost after the agent went offline: ok = %v, err = %v", ok, err)
	}
}
