//go:build integration

package projects_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/projects"
	"github.com/iykeevans/konduite/backend/internal/store"
)

type auditRow struct {
	workspaceID, actorID, resourceID uuid.UUID
	actorType, action, resourceType  string
	metadata                         map[string]any
}

func auditRows(t *testing.T, pool *pgxpool.Pool) []auditRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT workspace_id, actor_type, actor_id, action, resource_type, resource_id, metadata
		FROM audit_logs WHERE resource_type = 'project' ORDER BY created_at, action`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var (
			a   auditRow
			raw []byte
		)
		if err := rows.Scan(&a.workspaceID, &a.actorType, &a.actorID, &a.action, &a.resourceType, &a.resourceID, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &a.metadata); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func seedWorkspace(t *testing.T, pool *pgxpool.Pool, owner uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
		"ws-"+uuid.NewString()[:8], owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	addWorkspaceMember(t, pool, id, owner, authz.RoleOwner)
	return id
}

func addWorkspaceMember(t *testing.T, pool *pgxpool.Pool, workspaceID, userID uuid.UUID, role authz.Role) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
		workspaceID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func addProjectMember(t *testing.T, pool *pgxpool.Pool, projectID, userID uuid.UUID, role authz.ProjectRole) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`,
		projectID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func mustCreate(t *testing.T, repo projects.Repository, n projects.NewProject) projects.Project {
	t.Helper()
	p, err := repo.Create(t.Context(), n)
	if err != nil {
		t.Fatalf("create %s: %v", n.Name, err)
	}
	return p
}

func TestPostgresCreateStoresProjectMaintainerAndAuditEntry(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner, pm := uuid.New(), uuid.New()
	ws := seedWorkspace(t, pool, owner)
	addWorkspaceMember(t, pool, ws, pm, authz.RoleProjectManager)

	p := mustCreate(t, repo, projects.NewProject{
		WorkspaceID: ws, CreatorID: pm, Name: "Konduite", Description: "Backend",
		RepositoryURL: "git@github.com:acme/konduite.git",
	})

	if p.WorkspaceID != ws || p.Name != "Konduite" || p.Description != "Backend" ||
		p.RepositoryURL != "git@github.com:acme/konduite.git" || p.CreatedBy != pm || p.CreatedAt.IsZero() {
		t.Errorf("project = %+v, want Konduite in ws created by the project manager", p)
	}
	m, err := authz.StoreMemberships(store.New(pool)).ProjectMembership(t.Context(), p.ID, pm)
	if err != nil || m.ProjectRole != authz.ProjectRoleMaintainer {
		t.Errorf("creator's project membership = %+v, %v; want maintainer", m, err)
	}
	audit := auditRows(t, pool)
	if len(audit) != 1 {
		t.Fatalf("%d project audit rows, want 1", len(audit))
	}
	a := audit[0]
	if a.workspaceID != ws || a.actorType != "user" || a.actorID != pm || a.action != "project.created" ||
		a.resourceID != p.ID || a.metadata["name"] != "Konduite" {
		t.Errorf("audit = %+v, want project.created by the creator", a)
	}
}

func TestPostgresCreateWithoutRepositoryURLStoresNull(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner := uuid.New()
	p := mustCreate(t, repo, projects.NewProject{WorkspaceID: seedWorkspace(t, pool, owner), CreatorID: owner, Name: "K"})

	var isNull bool
	if err := pool.QueryRow(t.Context(), `SELECT repository_url IS NULL FROM projects WHERE id = $1`, p.ID).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if !isNull || p.RepositoryURL != "" {
		t.Errorf("repository_url null = %v, domain value %q; want NULL and empty", isNull, p.RepositoryURL)
	}
}

func TestPostgresCreateWithATakenNameStoresNothing(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner := uuid.New()
	ws := seedWorkspace(t, pool, owner)
	mustCreate(t, repo, projects.NewProject{WorkspaceID: ws, CreatorID: owner, Name: "Konduite"})

	_, err := repo.Create(t.Context(), projects.NewProject{WorkspaceID: ws, CreatorID: owner, Name: "Konduite"})

	if !errors.Is(err, projects.ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
	var projectsLeft, membersLeft int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM projects), (SELECT count(*) FROM project_members)`).
		Scan(&projectsLeft, &membersLeft); err != nil {
		t.Fatal(err)
	}
	if projectsLeft != 1 || membersLeft != 1 || len(auditRows(t, pool)) != 1 {
		t.Errorf("%d projects, %d members after a conflict, want only the first project's rows", projectsLeft, membersLeft)
	}

	other := seedWorkspace(t, pool, owner)
	if _, err := repo.Create(t.Context(), projects.NewProject{WorkspaceID: other, CreatorID: owner, Name: "Konduite"}); err != nil {
		t.Errorf("same name in another workspace: %v, want allowed", err)
	}
}

func TestPostgresListReturnsOnlyVisibleProjects(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner, admin, dev, pm, outsider := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ws := seedWorkspace(t, pool, owner)
	addWorkspaceMember(t, pool, ws, admin, authz.RoleAdmin)
	addWorkspaceMember(t, pool, ws, dev, authz.RoleDeveloper)
	addWorkspaceMember(t, pool, ws, pm, authz.RoleProjectManager)

	ids := map[string]uuid.UUID{}
	for _, name := range []string{"delta", "alpha", "charlie", "bravo"} {
		ids[name] = mustCreate(t, repo, projects.NewProject{WorkspaceID: ws, CreatorID: owner, Name: name}).ID
	}
	addProjectMember(t, pool, ids["bravo"], dev, authz.ProjectRoleViewer)
	addProjectMember(t, pool, ids["delta"], dev, authz.ProjectRoleContributor)
	mustCreate(t, repo, projects.NewProject{WorkspaceID: ws, CreatorID: pm, Name: "echo"})

	elsewhere := seedWorkspace(t, pool, outsider)
	addWorkspaceMember(t, pool, elsewhere, dev, authz.RoleDeveloper)
	foreign := mustCreate(t, repo, projects.NewProject{WorkspaceID: elsewhere, CreatorID: outsider, Name: "aaa-foreign"})
	addProjectMember(t, pool, foreign.ID, dev, authz.ProjectRoleViewer)

	tests := []struct {
		name string
		user uuid.UUID
		want []string
	}{
		{"owner sees all", owner, []string{"alpha", "bravo", "charlie", "delta", "echo"}},
		{"admin sees all", admin, []string{"alpha", "bravo", "charlie", "delta", "echo"}},
		{"developer sees project roles", dev, []string{"bravo", "delta"}},
		{"creator sees own project", pm, []string{"echo"}},
		{"non-member sees nothing", outsider, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.List(t.Context(), ws, tt.user, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			if n := names(got); !slices.Equal(n, tt.want) && !(len(n) == 0 && len(tt.want) == 0) {
				t.Errorf("list = %v, want %v", n, tt.want)
			}
		})
	}
}

func TestPostgresListPagesByName(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner := uuid.New()
	ws := seedWorkspace(t, pool, owner)
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		mustCreate(t, repo, projects.NewProject{WorkspaceID: ws, CreatorID: owner, Name: name})
	}

	first, err := repo.List(t.Context(), ws, owner, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := repo.List(t.Context(), ws, owner, first[len(first)-1].Name, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(names(first), names(rest)...); !slices.Equal(got, []string{"alpha", "bravo", "charlie"}) {
		t.Errorf("pages = %v, want alpha bravo | charlie", got)
	}
}

func TestPostgresGet(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner := uuid.New()
	p := mustCreate(t, repo, projects.NewProject{WorkspaceID: seedWorkspace(t, pool, owner), CreatorID: owner, Name: "K"})

	if got, err := repo.Get(t.Context(), p.ID); err != nil || got.ID != p.ID || got.Name != "K" {
		t.Errorf("get = %+v, %v; want K", got, err)
	}
	if _, err := repo.Get(t.Context(), uuid.New()); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("get missing: err = %v, want ErrNotFound", err)
	}
}

func TestPostgresUpdateChangesTheProjectAndAuditsIt(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner, admin := uuid.New(), uuid.New()
	ws := seedWorkspace(t, pool, owner)
	addWorkspaceMember(t, pool, ws, admin, authz.RoleAdmin)
	p := mustCreate(t, repo, projects.NewProject{
		WorkspaceID: ws, CreatorID: owner, Name: "K", Description: "old", RepositoryURL: "https://github.com/a/k",
	})

	got, err := repo.Update(t.Context(), p.ID, admin, projects.UpdateInput{
		Name: ptr("K"), Description: ptr("new"), RepositoryURL: ptr(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "K" || got.Description != "new" || got.RepositoryURL != "" || !got.UpdatedAt.After(p.UpdatedAt) {
		t.Errorf("project = %+v, want new description, no url, updated_at moved", got)
	}
	audit := auditRows(t, pool)
	if len(audit) != 2 {
		t.Fatalf("%d audit rows, want created + updated", len(audit))
	}
	a := audit[1]
	changes, _ := a.metadata["changes"].(map[string]any)
	desc, _ := changes["description"].(map[string]any)
	url, _ := changes["repository_url"].(map[string]any)
	if a.action != "project.updated" || a.actorID != admin || a.resourceID != p.ID || a.workspaceID != ws ||
		desc["from"] != "old" || desc["to"] != "new" || url["to"] != "" || changes["name"] != nil {
		t.Errorf("audit = %+v, want project.updated by the admin recording description and url only", a)
	}
}

func TestPostgresUpdateWithNoChangeWritesNothing(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner := uuid.New()
	p := mustCreate(t, repo, projects.NewProject{WorkspaceID: seedWorkspace(t, pool, owner), CreatorID: owner, Name: "K"})

	got, err := repo.Update(t.Context(), p.ID, owner, projects.UpdateInput{Name: ptr("K"), RepositoryURL: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(p.UpdatedAt) || len(auditRows(t, pool)) != 1 {
		t.Errorf("a no-op update moved updated_at or wrote an audit row")
	}
}

func TestPostgresUpdateErrors(t *testing.T) {
	pool := dbtest.New(t)
	repo := projects.NewPostgresRepository(pool)
	owner := uuid.New()
	ws := seedWorkspace(t, pool, owner)
	mustCreate(t, repo, projects.NewProject{WorkspaceID: ws, CreatorID: owner, Name: "alpha"})
	bravo := mustCreate(t, repo, projects.NewProject{WorkspaceID: ws, CreatorID: owner, Name: "bravo"})

	if _, err := repo.Update(t.Context(), bravo.ID, owner, projects.UpdateInput{Name: ptr("alpha")}); !errors.Is(err, projects.ErrNameTaken) {
		t.Errorf("taken name: err = %v, want ErrNameTaken", err)
	}
	if _, err := repo.Update(t.Context(), uuid.New(), owner, projects.UpdateInput{Name: ptr("x")}); !errors.Is(err, projects.ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
	if n := len(auditRows(t, pool)); n != 2 {
		t.Errorf("%d audit rows, want only the two creates", n)
	}
}
