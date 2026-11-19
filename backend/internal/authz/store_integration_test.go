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

	a := authz.New(authz.StoreMemberships(store.New(pool)))

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
