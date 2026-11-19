package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

type fakeMemberships map[[2]uuid.UUID]authz.Role

func (f fakeMemberships) Role(_ context.Context, workspaceID, userID uuid.UUID) (authz.Role, error) {
	role, ok := f[[2]uuid.UUID{workspaceID, userID}]
	if !ok {
		return "", authz.ErrNotMember
	}
	return role, nil
}

type failingMemberships struct{ err error }

func (f failingMemberships) Role(context.Context, uuid.UUID, uuid.UUID) (authz.Role, error) {
	return "", f.err
}

func user(id uuid.UUID) auth.Principal { return auth.Principal{Kind: auth.KindUser, ID: id} }

func TestCanRoleByAction(t *testing.T) {
	const (
		allow  = errs.Kind("")
		forbid = errs.KindForbidden
	)
	roles := []authz.Role{authz.RoleOwner, authz.RoleAdmin, authz.RoleDeveloper, authz.RoleProjectManager}
	tests := []struct {
		action authz.Action
		want   [4]errs.Kind // owner, admin, developer, project_manager
	}{
		{authz.ActionViewWorkspace, [4]errs.Kind{allow, allow, allow, allow}},
		{authz.ActionUpdateWorkspace, [4]errs.Kind{allow, allow, forbid, forbid}},
		{authz.ActionDeleteWorkspace, [4]errs.Kind{allow, forbid, forbid, forbid}},
		{authz.ActionViewMembers, [4]errs.Kind{allow, allow, allow, allow}},
		{authz.ActionManageMembers, [4]errs.Kind{allow, allow, forbid, forbid}},
		{authz.ActionViewProjects, [4]errs.Kind{allow, allow, allow, allow}},
		{authz.ActionCreateProject, [4]errs.Kind{allow, allow, forbid, allow}},
		{authz.ActionRegisterRuntime, [4]errs.Kind{allow, allow, allow, forbid}},
		{authz.ActionViewAuditLog, [4]errs.Kind{allow, allow, forbid, forbid}},
	}
	for _, tt := range tests {
		for i, role := range roles {
			t.Run(string(tt.action)+"/"+string(role), func(t *testing.T) {
				ws, uid := uuid.New(), uuid.New()
				a := authz.New(fakeMemberships{{ws, uid}: role})

				err := a.Can(t.Context(), user(uid), tt.action, authz.Workspace(ws))

				assertKind(t, err, tt.want[i])
			})
		}
	}
}

func TestCanDeniesWithoutLeakingExistence(t *testing.T) {
	ws, other, member, outsider := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	a := authz.New(fakeMemberships{{ws, member}: authz.RoleOwner, {other, outsider}: authz.RoleOwner})

	tests := []struct {
		name      string
		principal auth.Principal
		resource  authz.Resource
		want      errs.Kind
	}{
		{"member of another workspace", user(outsider), authz.Workspace(ws), errs.KindNotFound},
		{"workspace that does not exist", user(member), authz.Workspace(uuid.New()), errs.KindNotFound},
		{"unknown user", user(uuid.New()), authz.Workspace(ws), errs.KindNotFound},
		{"non-user principal", auth.Principal{Kind: "runtime", ID: member}, authz.Workspace(ws), errs.KindNotFound},
		{"principal without id", auth.Principal{Kind: auth.KindUser}, authz.Workspace(ws), errs.KindUnauthenticated},
	}
	var msgs []string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, action := range []authz.Action{authz.ActionViewWorkspace, authz.ActionDeleteWorkspace} {
				err := a.Can(t.Context(), tt.principal, action, tt.resource)
				assertKind(t, err, tt.want)
				if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
					msgs = append(msgs, e.Message)
				}
			}
		})
	}
	for _, m := range msgs[1:] {
		if m != msgs[0] {
			t.Errorf("not-found messages differ (%q vs %q), which tells callers why they were refused", msgs[0], m)
		}
	}
}

func TestCanDeniesByDefault(t *testing.T) {
	ws, uid := uuid.New(), uuid.New()

	t.Run("unknown action", func(t *testing.T) {
		a := authz.New(fakeMemberships{{ws, uid}: authz.RoleOwner})
		assertKind(t, a.Can(t.Context(), user(uid), "workspace.launch_missiles", authz.Workspace(ws)), errs.KindForbidden)
	})
	t.Run("unknown role", func(t *testing.T) {
		a := authz.New(fakeMemberships{{ws, uid}: "superuser"})
		assertKind(t, a.Can(t.Context(), user(uid), authz.ActionViewWorkspace, authz.Workspace(ws)), errs.KindForbidden)
	})
}

func TestCanSurfacesLookupFailuresAsInternal(t *testing.T) {
	boom := errors.New("connection refused")
	a := authz.New(failingMemberships{boom})

	err := a.Can(t.Context(), user(uuid.New()), authz.ActionViewWorkspace, authz.Workspace(uuid.New()))

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the lookup error", err)
	}
	if _, ok := errs.As(err); ok {
		t.Errorf("err = %v is a domain error; a failed lookup must become a 500, not a 403/404", err)
	}
}

func TestCanRejectsResourceWithoutWorkspace(t *testing.T) {
	a := authz.New(fakeMemberships{})

	err := a.Can(t.Context(), user(uuid.New()), authz.ActionViewWorkspace, authz.Resource{})

	if err == nil {
		t.Fatal("err = nil, want an error for a resource with no workspace")
	}
	if _, ok := errs.As(err); ok {
		t.Errorf("err = %v is a domain error; a missing workspace id is a caller bug and must be a 500", err)
	}
}

func assertKind(t *testing.T, err error, want errs.Kind) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("err = %v, want allowed", err)
		}
		return
	}
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("err = %v, want errs kind %q", err, want)
	}
	if e.Kind != want {
		t.Fatalf("kind = %q, want %q", e.Kind, want)
	}
}
