//go:build integration

package projectmembers_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/projectmembers"
	"github.com/iykeevans/konduite/backend/internal/projects"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/backend/internal/workspaces"
)

type pgFixture struct {
	pool    *pgxpool.Pool
	svc     *projectmembers.Service
	ws      uuid.UUID
	project uuid.UUID
	owner   auth.Principal
	// lead created the project, so is its only maintainer.
	lead auth.Principal
}

func newPGFixture(t *testing.T) pgFixture {
	t.Helper()
	pool := dbtest.New(t)
	owner := user(uuid.New())
	ws, err := workspaces.NewPostgresRepository(pool).Create(t.Context(), workspaces.NewWorkspace{
		Name: "Acme", Slug: "acme", OwnerID: owner.ID, OwnerEmail: "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	f := pgFixture{pool: pool, ws: ws.ID, owner: owner}
	f.lead = f.join(t, "lead@example.com", authz.RoleProjectManager)
	p, err := projects.NewPostgresRepository(pool).Create(t.Context(), projects.NewProject{
		WorkspaceID: ws.ID, CreatorID: f.lead.ID, Name: "Konduite",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.project = p.ID
	az := authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))
	f.svc = projectmembers.NewService(projectmembers.NewPostgresRepository(pool), az)
	return f
}

func (f pgFixture) join(t *testing.T, email string, role authz.Role) auth.Principal {
	t.Helper()
	return joinWorkspace(t, f.pool, f.ws, email, role)
}

func joinWorkspace(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID, email string, role authz.Role) auth.Principal {
	t.Helper()
	p := user(uuid.New())
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO workspace_members (workspace_id, user_id, role, email) VALUES ($1, $2, $3, $4)`,
		ws, p.ID, role, email); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f pgFixture) leaveWorkspace(t *testing.T, userID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, f.ws, userID); err != nil {
		t.Fatal(err)
	}
}

type auditRow struct {
	workspaceID, actorID, resourceID uuid.UUID
	action, resourceType             string
	metadata                         map[string]any
}

func memberAudit(t *testing.T, pool *pgxpool.Pool) []auditRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT workspace_id, actor_id, action, resource_type, resource_id, metadata
		FROM audit_logs WHERE action LIKE 'project.member_%' ORDER BY created_at, id`)
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
		if err := rows.Scan(&a.workspaceID, &a.actorID, &a.action, &a.resourceType, &a.resourceID, &raw); err != nil {
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

func TestPostgresAddStoresTheMemberWithAnAuditEntry(t *testing.T) {
	f := newPGFixture(t)
	dev := f.join(t, "dev@example.com", authz.RoleDeveloper)

	m, err := f.svc.Add(t.Context(), f.lead, f.project, dev.ID, "contributor")
	if err != nil {
		t.Fatal(err)
	}
	if m.UserID != dev.ID || m.Role != authz.ProjectRoleContributor || m.Email != "dev@example.com" ||
		m.WorkspaceRole != authz.RoleDeveloper || m.JoinedAt.IsZero() {
		t.Errorf("member = %+v, want dev as contributor with email and workspace role", m)
	}

	audit := memberAudit(t, f.pool)
	if len(audit) != 1 {
		t.Fatalf("%d audit rows, want 1", len(audit))
	}
	a := audit[0]
	if a.workspaceID != f.ws || a.actorID != f.lead.ID || a.action != "project.member_added" ||
		a.resourceType != "project" || a.resourceID != f.project ||
		a.metadata["user_id"] != dev.ID.String() || a.metadata["role"] != "contributor" {
		t.Errorf("audit = %+v, want project.member_added of dev by the lead", a)
	}
}

func TestPostgresAddRejectsExistingMembersAndPeopleOutsideTheWorkspace(t *testing.T) {
	f := newPGFixture(t)
	dev := f.join(t, "dev@example.com", authz.RoleDeveloper)
	if _, err := f.svc.Add(t.Context(), f.lead, f.project, dev.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	other, err := workspaces.NewPostgresRepository(f.pool).Create(t.Context(), workspaces.NewWorkspace{
		Name: "Other", Slug: "other", OwnerID: uuid.New(), OwnerEmail: "boss@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := joinWorkspace(t, f.pool, other.ID, "elsewhere@example.com", authz.RoleDeveloper)

	tests := []struct {
		name   string
		userID uuid.UUID
		want   errs.Kind
	}{
		{"already a member", dev.ID, errs.KindConflict},
		{"creator is already a maintainer", f.lead.ID, errs.KindConflict},
		{"member of another workspace", elsewhere.ID, errs.KindInvalid},
		{"unknown user", uuid.New(), errs.KindInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Add(t.Context(), f.lead, f.project, tt.userID, "contributor")
			wantKind(t, err, tt.want)
		})
	}
	if n := len(memberAudit(t, f.pool)); n != 1 {
		t.Errorf("%d audit rows, want only the first add", n)
	}
}

func TestPostgresListShowsEmailsAndHidesPeopleWhoLeftTheWorkspace(t *testing.T) {
	f := newPGFixture(t)
	dev := f.join(t, "dev@example.com", authz.RoleDeveloper)
	gone := f.join(t, "gone@example.com", authz.RoleDeveloper)
	for _, id := range []uuid.UUID{dev.ID, gone.ID} {
		if _, err := f.svc.Add(t.Context(), f.lead, f.project, id, "viewer"); err != nil {
			t.Fatal(err)
		}
	}
	f.leaveWorkspace(t, gone.ID)

	page, err := f.svc.List(t.Context(), dev, f.project, projectmembers.Cursor{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != f.lead.ID || page.Items[0].Email != "lead@example.com" ||
		page.Items[0].WorkspaceRole != authz.RoleProjectManager || page.Next == nil {
		t.Fatalf("first page = %+v, want the lead with email and a next cursor", page)
	}
	page, err = f.svc.List(t.Context(), dev, f.project, *page.Next, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != dev.ID || page.Next != nil {
		t.Fatalf("second page = %+v, want only dev", page)
	}
}

func TestPostgresChangeRoleAuditsOnlyRealChanges(t *testing.T) {
	f := newPGFixture(t)
	dev := f.join(t, "dev@example.com", authz.RoleDeveloper)
	if _, err := f.svc.Add(t.Context(), f.lead, f.project, dev.ID, "viewer"); err != nil {
		t.Fatal(err)
	}

	m, err := f.svc.ChangeRole(t.Context(), f.lead, f.project, dev.ID, "maintainer")
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != authz.ProjectRoleMaintainer || m.Email != "dev@example.com" {
		t.Errorf("member = %+v, want dev as maintainer", m)
	}
	if _, err := f.svc.ChangeRole(t.Context(), f.lead, f.project, dev.ID, "maintainer"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Remove(t.Context(), dev, f.project, f.lead.ID); err != nil {
		t.Fatalf("new maintainer removes the lead: %v", err)
	}

	audit := memberAudit(t, f.pool)
	if len(audit) != 3 {
		t.Fatalf("%d audit rows, want add, role change and removal", len(audit))
	}
	if c := audit[1]; c.action != "project.member_role_changed" || c.metadata["from"] != "viewer" || c.metadata["to"] != "maintainer" {
		t.Errorf("role change audit = %+v", c)
	}
	if r := audit[2]; r.action != "project.member_removed" || r.actorID != dev.ID ||
		r.metadata["user_id"] != f.lead.ID.String() || r.metadata["role"] != "maintainer" || r.metadata["self"] != false {
		t.Errorf("removal audit = %+v", r)
	}
}

func TestPostgresLastMaintainerGuard(t *testing.T) {
	t.Run("the only maintainer cannot step down or leave", func(t *testing.T) {
		f := newPGFixture(t)
		_, err := f.svc.ChangeRole(t.Context(), f.lead, f.project, f.lead.ID, "contributor")
		wantKind(t, err, errs.KindConflict)
		wantKind(t, f.svc.Remove(t.Context(), f.lead, f.project, f.lead.ID), errs.KindConflict)
	})
	t.Run("a maintainer who left the workspace does not count", func(t *testing.T) {
		f := newPGFixture(t)
		gone := f.join(t, "gone@example.com", authz.RoleDeveloper)
		if _, err := f.svc.Add(t.Context(), f.lead, f.project, gone.ID, "maintainer"); err != nil {
			t.Fatal(err)
		}
		f.leaveWorkspace(t, gone.ID)
		wantKind(t, f.svc.Remove(t.Context(), f.lead, f.project, f.lead.ID), errs.KindConflict)
		if err := f.svc.Remove(t.Context(), f.lead, f.project, gone.ID); err != nil {
			t.Errorf("removing the stale maintainer: %v", err)
		}
	})
	t.Run("a workspace owner can remove the last maintainer", func(t *testing.T) {
		f := newPGFixture(t)
		if err := f.svc.Remove(t.Context(), f.owner, f.project, f.lead.ID); err != nil {
			t.Fatal(err)
		}
		page, err := f.svc.List(t.Context(), f.owner, f.project, projectmembers.Cursor{}, 10)
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("members = %+v, %v; want none", page.Items, err)
		}
		if err := f.svc.Remove(t.Context(), f.lead, f.project, f.lead.ID); err == nil {
			t.Error("removed lead can still act on the project")
		}
	})
}

func TestPostgresConcurrentStepDownsLeaveOneMaintainer(t *testing.T) {
	f := newPGFixture(t)
	second := f.join(t, "second@example.com", authz.RoleDeveloper)
	if _, err := f.svc.Add(t.Context(), f.lead, f.project, second.ID, "maintainer"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, p := range []auth.Principal{f.lead, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.svc.ChangeRole(t.Context(), p, f.project, p.ID, "viewer")
		}()
	}
	wg.Wait()

	failed := 0
	for _, err := range results {
		if err != nil {
			wantKind(t, err, errs.KindConflict)
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("results = %v, want exactly one step-down refused", results)
	}
	var maintainers int
	if err := f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM project_members WHERE project_id = $1 AND role = 'maintainer'`, f.project).Scan(&maintainers); err != nil {
		t.Fatal(err)
	}
	if maintainers != 1 {
		t.Errorf("%d maintainers left, want 1", maintainers)
	}
}
