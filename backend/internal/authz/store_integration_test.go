//go:build integration

package authz_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/store"
)

func TestCanWithStoreMemberships(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()

	acme, globex := uuid.New(), uuid.New()
	owner, dev, outsider := uuid.New(), uuid.New(), uuid.New()
	for _, ws := range []struct {
		id   uuid.UUID
		slug string
	}{{acme, "acme"}, {globex, "globex"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1, $2, $2, $3)`, ws.id, ws.slug, owner); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []struct {
		ws, user uuid.UUID
		role     authz.Role
	}{{acme, owner, authz.RoleOwner}, {acme, dev, authz.RoleDeveloper}, {globex, outsider, authz.RoleOwner}} {
		if _, err := pool.Exec(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`, m.ws, m.user, m.role); err != nil {
			t.Fatal(err)
		}
	}

	a := authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))

	tests := []struct {
		name   string
		user   uuid.UUID
		action authz.Action
		ws     uuid.UUID
		want   errs.Kind
	}{
		{"owner deletes own workspace", owner, authz.ActionDeleteWorkspace, acme, ""},
		{"developer views workspace", dev, authz.ActionViewWorkspace, acme, ""},
		{"developer cannot delete", dev, authz.ActionDeleteWorkspace, acme, errs.KindForbidden},
		{"owner of another workspace", outsider, authz.ActionViewWorkspace, acme, errs.KindNotFound},
		{"workspace that does not exist", owner, authz.ActionViewWorkspace, uuid.New(), errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertKind(t, a.Can(ctx, user(tt.user), tt.action, authz.Workspace(tt.ws)), tt.want)
		})
	}
}

func TestCanProjectWithStoreMemberships(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()

	acme, globex := uuid.New(), uuid.New()
	api, web, rival := uuid.New(), uuid.New(), uuid.New()
	admin, maintainer, viewer, bystander, outsider := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []struct {
		id   uuid.UUID
		slug string
	}{{acme, "acme"}, {globex, "globex"}} {
		exec(`INSERT INTO workspaces (id, name, slug, created_by) VALUES ($1, $2, $2, $3)`, ws.id, ws.slug, admin)
	}
	for _, m := range []struct {
		ws, user uuid.UUID
		role     authz.Role
	}{
		{acme, admin, authz.RoleAdmin}, {acme, maintainer, authz.RoleDeveloper}, {acme, viewer, authz.RoleProjectManager},
		{acme, bystander, authz.RoleDeveloper}, {globex, outsider, authz.RoleOwner},
	} {
		exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`, m.ws, m.user, m.role)
	}
	for _, p := range []struct {
		id, ws uuid.UUID
		name   string
	}{{api, acme, "api"}, {web, acme, "web"}, {rival, globex, "rival"}} {
		exec(`INSERT INTO projects (id, workspace_id, name, created_by) VALUES ($1, $2, $3, $4)`, p.id, p.ws, p.name, admin)
	}
	for _, m := range []struct {
		project, user uuid.UUID
		role          authz.ProjectRole
	}{
		{api, maintainer, authz.ProjectRoleMaintainer}, {api, viewer, authz.ProjectRoleViewer},
		{api, outsider, authz.ProjectRoleMaintainer},
	} {
		exec(`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`, m.project, m.user, m.role)
	}

	a := authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))

	tests := []struct {
		name    string
		user    uuid.UUID
		action  authz.Action
		project uuid.UUID
		want    errs.Kind
	}{
		{"maintainer manages members", maintainer, authz.ActionManageProjectMembers, api, ""},
		{"viewer views project", viewer, authz.ActionViewProject, api, ""},
		{"viewer cannot contribute", viewer, authz.ActionContribute, api, errs.KindForbidden},
		{"admin maintains a project without a member row", admin, authz.ActionDeleteProject, web, ""},
		{"maintainer of one project has no role in another", maintainer, authz.ActionViewProject, web, errs.KindForbidden},
		{"workspace member without project role", bystander, authz.ActionViewProject, api, errs.KindForbidden},
		{"project member outside the workspace", outsider, authz.ActionViewProject, api, errs.KindNotFound},
		{"project in another workspace", admin, authz.ActionViewProject, rival, errs.KindNotFound},
		{"project that does not exist", admin, authz.ActionViewProject, uuid.New(), errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertKind(t, a.Can(ctx, user(tt.user), tt.action, authz.Project(tt.project)), tt.want)
		})
	}
}
