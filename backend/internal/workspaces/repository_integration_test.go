//go:build integration

package workspaces_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/backend/internal/workspaces"
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
		FROM audit_logs ORDER BY created_at, action`)
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

func TestPostgresCreateStoresWorkspaceOwnerAndAuditEntry(t *testing.T) {
	pool := dbtest.New(t)
	repo := workspaces.NewPostgresRepository(pool)
	owner := uuid.New()

	ws, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Acme", Slug: "acme", OwnerID: owner})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Name != "Acme" || ws.Slug != "acme" || ws.CreatedBy != owner || ws.Role != authz.RoleOwner || ws.CreatedAt.IsZero() {
		t.Errorf("workspace = %+v, want acme created and owned by owner", ws)
	}
	role, err := authz.StoreMemberships(store.New(pool)).Role(t.Context(), ws.ID, owner)
	if err != nil || role != authz.RoleOwner {
		t.Errorf("membership role = %q, %v; want owner", role, err)
	}
	audit := auditRows(t, pool)
	if len(audit) != 1 {
		t.Fatalf("%d audit rows, want 1", len(audit))
	}
	a := audit[0]
	if a.workspaceID != ws.ID || a.actorType != "user" || a.actorID != owner || a.action != "workspace.created" ||
		a.resourceType != "workspace" || a.resourceID != ws.ID || a.metadata["slug"] != "acme" || a.metadata["name"] != "Acme" {
		t.Errorf("audit = %+v, want workspace.created by the owner with name and slug", a)
	}
}

func TestPostgresCreateWithATakenSlugStoresNothing(t *testing.T) {
	pool := dbtest.New(t)
	repo := workspaces.NewPostgresRepository(pool)
	if _, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Acme", Slug: "acme", OwnerID: uuid.New()}); err != nil {
		t.Fatal(err)
	}

	_, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Other", Slug: "acme", OwnerID: uuid.New()})

	if !errors.Is(err, workspaces.ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
	var workspacesLeft, membersLeft int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM workspaces), (SELECT count(*) FROM workspace_members)`).
		Scan(&workspacesLeft, &membersLeft); err != nil {
		t.Fatal(err)
	}
	if workspacesLeft != 1 || membersLeft != 1 || len(auditRows(t, pool)) != 1 {
		t.Errorf("%d workspaces, %d members after a conflict, want only the first workspace's rows", workspacesLeft, membersLeft)
	}
}

func TestPostgresGetAndListSeeOnlyMembership(t *testing.T) {
	pool := dbtest.New(t)
	repo := workspaces.NewPostgresRepository(pool)
	me, other := uuid.New(), uuid.New()
	var mine []workspaces.Workspace
	for _, slug := range []string{"charlie", "alpha", "bravo"} {
		ws, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: slug, Slug: slug, OwnerID: me})
		if err != nil {
			t.Fatal(err)
		}
		mine = append(mine, ws)
	}
	theirs, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Secret", Slug: "secret", OwnerID: other})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := repo.Get(t.Context(), mine[0].ID, me); err != nil || got.Slug != "charlie" || got.Role != authz.RoleOwner {
		t.Errorf("get own = %+v, %v; want charlie as owner", got, err)
	}
	if _, err := repo.Get(t.Context(), theirs.ID, me); !errors.Is(err, workspaces.ErrNotFound) {
		t.Errorf("get theirs: err = %v, want ErrNotFound", err)
	}

	first, err := repo.List(t.Context(), me, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := repo.List(t.Context(), me, first[len(first)-1].Slug, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(slugs(first), slugs(rest)...); len(got) != 3 || got[0] != "alpha" || got[1] != "bravo" || got[2] != "charlie" {
		t.Errorf("pages = %v, want alpha bravo | charlie and nothing of other's", got)
	}
}

func TestPostgresUpdateChangesTheWorkspaceAndAuditsIt(t *testing.T) {
	pool := dbtest.New(t)
	repo := workspaces.NewPostgresRepository(pool)
	owner, admin := uuid.New(), uuid.New()
	ws, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Acme", Slug: "acme", OwnerID: owner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`,
		ws.ID, admin); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Update(t.Context(), ws.ID, admin, workspaces.UpdateInput{Name: ptr("Acme Inc"), Slug: ptr("acme")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme Inc" || got.Slug != "acme" || got.Role != authz.RoleAdmin || !got.UpdatedAt.After(ws.UpdatedAt) {
		t.Errorf("workspace = %+v, want renamed, seen by the admin, updated_at moved", got)
	}
	audit := auditRows(t, pool)
	if len(audit) != 2 {
		t.Fatalf("%d audit rows, want created + updated", len(audit))
	}
	a := audit[1]
	changes, _ := a.metadata["changes"].(map[string]any)
	name, _ := changes["name"].(map[string]any)
	if a.action != "workspace.updated" || a.actorID != admin || a.resourceID != ws.ID ||
		name["from"] != "Acme" || name["to"] != "Acme Inc" || changes["slug"] != nil {
		t.Errorf("audit = %+v, want workspace.updated by the admin recording only the name change", a)
	}
}

func TestPostgresUpdateWithNoChangeWritesNothing(t *testing.T) {
	pool := dbtest.New(t)
	repo := workspaces.NewPostgresRepository(pool)
	owner := uuid.New()
	ws, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Acme", Slug: "acme", OwnerID: owner})
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.Update(t.Context(), ws.ID, owner, workspaces.UpdateInput{Name: ptr("Acme")})
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(ws.UpdatedAt) || len(auditRows(t, pool)) != 1 {
		t.Errorf("a no-op update moved updated_at or wrote an audit row")
	}
}

func TestPostgresUpdateErrors(t *testing.T) {
	pool := dbtest.New(t)
	repo := workspaces.NewPostgresRepository(pool)
	owner := uuid.New()
	if _, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Acme", Slug: "acme", OwnerID: owner}); err != nil {
		t.Fatal(err)
	}
	bolt, err := repo.Create(t.Context(), workspaces.NewWorkspace{Name: "Bolt", Slug: "bolt", OwnerID: owner})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Update(t.Context(), bolt.ID, owner, workspaces.UpdateInput{Slug: ptr("acme")}); !errors.Is(err, workspaces.ErrSlugTaken) {
		t.Errorf("taken slug: err = %v, want ErrSlugTaken", err)
	}
	if _, err := repo.Update(t.Context(), uuid.New(), owner, workspaces.UpdateInput{Name: ptr("x")}); !errors.Is(err, workspaces.ErrNotFound) {
		t.Errorf("unknown workspace: err = %v, want ErrNotFound", err)
	}
	if n := len(auditRows(t, pool)); n != 2 {
		t.Errorf("%d audit rows, want only the two creates", n)
	}
}
