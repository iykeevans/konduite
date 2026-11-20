//go:build integration

package conversations_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/store"
)

func seedWorkspace(t *testing.T, pool *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(t.Context(), `INSERT INTO workspaces (name, slug, created_by) VALUES ($1, $1, $2) RETURNING id`,
		slug, uuid.New()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedProject(t *testing.T, pool *pgxpool.Pool, workspace uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(t.Context(), `INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, 'api', $2) RETURNING id`,
		workspace, uuid.New()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedMember(t *testing.T, pool *pgxpool.Pool, workspace uuid.UUID, role authz.Role) uuid.UUID {
	t.Helper()
	user := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
		workspace, user, role); err != nil {
		t.Fatal(err)
	}
	return user
}

func TestPostgresRepositoryCreatesAConversationWithItsCreator(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	ws := seedWorkspace(t, pool, "acme")
	project := seedProject(t, pool, ws)
	creator := uuid.New()
	repo := conversations.NewPostgresRepository(pool)

	created, err := repo.Create(ctx, conversations.NewConversation{WorkspaceID: ws, ProjectID: &project, Title: "Release", CreatedBy: creator})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != ws || got.ProjectID == nil || *got.ProjectID != project || got.Title != "Release" ||
		got.CreatedBy != creator || got.LastSeq != 0 || got.CreatedAt.IsZero() {
		t.Errorf("conversation = %+v", got)
	}
	if len(got.Participants) != 1 || got.Participants[0].Kind != conversations.KindUser || got.Participants[0].ID != creator {
		t.Errorf("participants = %+v, want the creator", got.Participants)
	}
	if len(created.Participants) != 1 {
		t.Errorf("Create returned participants %+v, want the creator", created.Participants)
	}

	if _, err := repo.Get(ctx, uuid.New()); !errors.Is(err, conversations.ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestPostgresRepositoryRejectsAProjectFromAnotherWorkspace(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	ws := seedWorkspace(t, pool, "acme")
	foreign := seedProject(t, pool, seedWorkspace(t, pool, "other"))
	repo := conversations.NewPostgresRepository(pool)

	for _, project := range []uuid.UUID{foreign, uuid.New()} {
		_, err := repo.Create(ctx, conversations.NewConversation{WorkspaceID: ws, ProjectID: &project, CreatedBy: uuid.New()})
		if !errors.Is(err, conversations.ErrProjectNotFound) {
			t.Errorf("project %v: err = %v, want ErrProjectNotFound", project, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d conversations stored, want 0", n)
	}
}

func TestPostgresRepositoryListsNewestFirstAcrossPages(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	ws := seedWorkspace(t, pool, "acme")
	other := seedWorkspace(t, pool, "other")
	project := seedProject(t, pool, ws)
	owner := seedMember(t, pool, ws, authz.RoleOwner)
	repo := conversations.NewPostgresRepository(pool)

	var want []uuid.UUID
	for i := range 5 {
		n := conversations.NewConversation{WorkspaceID: ws, CreatedBy: uuid.New()}
		if i%2 == 0 {
			n.ProjectID = &project
		}
		c, err := repo.Create(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, c.ID)
	}
	if _, err := repo.Create(ctx, conversations.NewConversation{WorkspaceID: other, CreatedBy: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	// Force a creation-time tie so the id tiebreak is exercised.
	if _, err := pool.Exec(ctx, `UPDATE conversations SET created_at = '2030-01-01T00:00:00Z' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return -slices.Compare(a[:], b[:]) })

	var got []uuid.UUID
	var after *conversations.Position
	for range 10 {
		page, err := repo.List(ctx, conversations.ListFilter{WorkspaceID: ws, ViewerID: owner, After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			got = append(got, c.ID)
		}
		last := page[len(page)-1]
		after = &conversations.Position{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if !slices.Equal(got, want) {
		t.Errorf("listed %v, want %v", got, want)
	}

	inProject, err := repo.List(ctx, conversations.ListFilter{WorkspaceID: ws, ProjectID: &project, ViewerID: owner, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(inProject) != 3 {
		t.Errorf("%d conversations in the project, want 3", len(inProject))
	}
}

func TestPostgresRepositoryListsProjectConversationsOnlyToWhoeverAuthzLetsViewTheProject(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	ws := seedWorkspace(t, pool, "acme")
	project := seedProject(t, pool, ws)
	repo := conversations.NewPostgresRepository(pool)
	az := authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))

	open, err := repo.Create(ctx, conversations.NewConversation{WorkspaceID: ws, CreatedBy: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	inProject, err := repo.Create(ctx, conversations.NewConversation{WorkspaceID: ws, ProjectID: &project, CreatedBy: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}

	type viewer struct {
		ws   authz.Role
		proj authz.ProjectRole
	}
	for _, v := range []viewer{
		{authz.RoleOwner, ""}, {authz.RoleAdmin, ""},
		{authz.RoleDeveloper, authz.ProjectRoleMaintainer}, {authz.RoleDeveloper, authz.ProjectRoleContributor},
		{authz.RoleProjectManager, authz.ProjectRoleViewer},
		{authz.RoleDeveloper, ""}, {authz.RoleProjectManager, ""},
	} {
		t.Run(string(v.ws)+"+"+string(v.proj), func(t *testing.T) {
			user := seedMember(t, pool, ws, v.ws)
			if v.proj != "" {
				if _, err := pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`,
					project, user, v.proj); err != nil {
					t.Fatal(err)
				}
			}
			canView := az.Can(ctx, auth.Principal{Kind: auth.KindUser, ID: user}, authz.ActionViewProject, authz.Project(project)) == nil

			page, err := repo.List(ctx, conversations.ListFilter{WorkspaceID: ws, ViewerID: user, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			want := []uuid.UUID{open.ID}
			if canView {
				want = []uuid.UUID{inProject.ID, open.ID}
			}
			var got []uuid.UUID
			for _, c := range page {
				got = append(got, c.ID)
			}
			if !slices.Equal(got, want) {
				t.Errorf("listed %v, want %v (authz project.view allowed = %v)", got, want, canView)
			}
		})
	}
}

func seedRuntimeAndAgent(t *testing.T, pool *pgxpool.Pool, workspace uuid.UUID) (runtime, agent uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime_instances (workspace_id, owner_id, name) VALUES ($1, $2, 'laptop') RETURNING id`,
		workspace, uuid.New()).Scan(&runtime); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agents (workspace_id, runtime_id, provider, name) VALUES ($1, $2, 'claude_code', 'claude') RETURNING id`,
		workspace, runtime).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	return runtime, agent
}

func TestPostgresRepositoryAddsAndRemovesParticipants(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	ws := seedWorkspace(t, pool, "acme")
	runtime, agent := seedRuntimeAndAgent(t, pool, ws)
	foreignRuntime, foreignAgent := seedRuntimeAndAgent(t, pool, seedWorkspace(t, pool, "other"))
	repo := conversations.NewPostgresRepository(pool)
	c, err := repo.Create(ctx, conversations.NewConversation{WorkspaceID: ws, CreatedBy: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}

	first, created, err := repo.AddParticipant(ctx, c.ID, ws, conversations.KindAgent, agent)
	if err != nil || !created || first.ID != agent || first.Kind != conversations.KindAgent {
		t.Fatalf("add agent = %+v, created %v, err %v", first, created, err)
	}
	again, created, err := repo.AddParticipant(ctx, c.ID, ws, conversations.KindAgent, agent)
	if err != nil || created || !again.JoinedAt.Equal(first.JoinedAt) {
		t.Errorf("re-add agent = %+v, created %v, err %v; want the stored row", again, created, err)
	}
	if _, created, err := repo.AddParticipant(ctx, c.ID, ws, conversations.KindRuntime, runtime); err != nil || !created {
		t.Errorf("add runtime: created %v, err %v", created, err)
	}
	for _, tc := range []struct {
		kind conversations.ParticipantKind
		id   uuid.UUID
	}{
		{conversations.KindAgent, foreignAgent}, {conversations.KindRuntime, foreignRuntime},
		{conversations.KindAgent, uuid.New()}, {conversations.KindRuntime, agent},
	} {
		if _, _, err := repo.AddParticipant(ctx, c.ID, ws, tc.kind, tc.id); !errors.Is(err, conversations.ErrParticipantNotInWorkspace) {
			t.Errorf("add %s %v: err = %v, want ErrParticipantNotInWorkspace", tc.kind, tc.id, err)
		}
	}

	for range 2 {
		if err := repo.RemoveParticipant(ctx, c.ID, conversations.KindAgent, agent); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Participants) != 2 || got.Participants[1].Kind != conversations.KindRuntime {
		t.Errorf("participants = %+v, want the creator and the runtime", got.Participants)
	}
}
