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

type fakeProject struct {
	workspace uuid.UUID
	roles     map[uuid.UUID]authz.ProjectRole
}

type fakeDirectory struct {
	fakeMemberships
	projects map[uuid.UUID]fakeProject
}

func (f fakeDirectory) ProjectMembership(ctx context.Context, projectID, userID uuid.UUID) (authz.ProjectMembership, error) {
	p, ok := f.projects[projectID]
	if !ok {
		return authz.ProjectMembership{}, authz.ErrNotMember
	}
	wsRole, err := f.Role(ctx, p.workspace, userID)
	if err != nil {
		return authz.ProjectMembership{}, err
	}
	return authz.ProjectMembership{WorkspaceID: p.workspace, WorkspaceRole: wsRole, ProjectRole: p.roles[userID]}, nil
}

type failingDirectory struct{ failingMemberships }

func (f failingDirectory) ProjectMembership(context.Context, uuid.UUID, uuid.UUID) (authz.ProjectMembership, error) {
	return authz.ProjectMembership{}, f.err
}

type fakeGrants map[[2]uuid.UUID][]authz.Action

func (f fakeGrants) ProjectActions(_ context.Context, principalID, projectID uuid.UUID) ([]authz.Action, error) {
	actions, ok := f[[2]uuid.UUID{principalID, projectID}]
	if !ok {
		return nil, authz.ErrNoGrant
	}
	return actions, nil
}

const kindAgent auth.Kind = "agent"

func TestCanProjectRoleByAction(t *testing.T) {
	const (
		allow  = errs.Kind("")
		forbid = errs.KindForbidden
	)
	type access struct {
		ws   authz.Role
		proj authz.ProjectRole
	}
	accesses := []access{
		{authz.RoleOwner, ""},
		{authz.RoleAdmin, ""},
		{authz.RoleDeveloper, authz.ProjectRoleMaintainer},
		{authz.RoleDeveloper, authz.ProjectRoleContributor},
		{authz.RoleProjectManager, authz.ProjectRoleViewer},
		{authz.RoleDeveloper, ""},
		{authz.RoleProjectManager, ""},
	}
	tests := []struct {
		action authz.Action
		want   [7]errs.Kind // owner, admin, maintainer, contributor, viewer, developer w/o project role, project_manager w/o project role
	}{
		{authz.ActionViewProject, [7]errs.Kind{allow, allow, allow, allow, allow, forbid, forbid}},
		{authz.ActionUpdateProject, [7]errs.Kind{allow, allow, allow, forbid, forbid, forbid, forbid}},
		{authz.ActionDeleteProject, [7]errs.Kind{allow, allow, allow, forbid, forbid, forbid, forbid}},
		{authz.ActionViewProjectMembers, [7]errs.Kind{allow, allow, allow, allow, allow, forbid, forbid}},
		{authz.ActionManageProjectMembers, [7]errs.Kind{allow, allow, allow, forbid, forbid, forbid, forbid}},
		{authz.ActionManageProjectAgents, [7]errs.Kind{allow, allow, allow, forbid, forbid, forbid, forbid}},
		{authz.ActionContribute, [7]errs.Kind{allow, allow, allow, allow, forbid, forbid, forbid}},
		// Workspace actions on a project resource use the role in the project's workspace.
		{authz.ActionViewWorkspace, [7]errs.Kind{allow, allow, allow, allow, allow, allow, allow}},
		{authz.ActionDeleteWorkspace, [7]errs.Kind{allow, forbid, forbid, forbid, forbid, forbid, forbid}},
	}
	for _, tt := range tests {
		for i, acc := range accesses {
			t.Run(string(tt.action)+"/"+string(acc.ws)+"+"+string(acc.proj), func(t *testing.T) {
				ws, proj, uid := uuid.New(), uuid.New(), uuid.New()
				dir := fakeDirectory{
					fakeMemberships: fakeMemberships{{ws, uid}: acc.ws},
					projects:        map[uuid.UUID]fakeProject{proj: {ws, map[uuid.UUID]authz.ProjectRole{uid: acc.proj}}},
				}
				a := authz.NewWithProjects(dir)

				assertKind(t, a.Can(t.Context(), user(uid), tt.action, authz.Project(proj)), tt.want[i])
			})
		}
	}
}

func TestCanProjectActionOnWorkspaceResourceIsDenied(t *testing.T) {
	ws, uid := uuid.New(), uuid.New()
	a := authz.New(fakeDirectory{fakeMemberships: fakeMemberships{{ws, uid}: authz.RoleOwner}})

	assertKind(t, a.Can(t.Context(), user(uid), authz.ActionViewProject, authz.Workspace(ws)), errs.KindForbidden)
}

func TestCanProjectDeniesWithoutLeakingExistence(t *testing.T) {
	ws, other := uuid.New(), uuid.New()
	proj := uuid.New()
	member, outsider, agent := uuid.New(), uuid.New(), uuid.New()
	a := authz.New(fakeDirectory{
		fakeMemberships: fakeMemberships{{ws, member}: authz.RoleOwner, {other, outsider}: authz.RoleOwner},
		projects: map[uuid.UUID]fakeProject{
			proj: {ws, map[uuid.UUID]authz.ProjectRole{outsider: authz.ProjectRoleMaintainer}},
		},
	}, authz.WithGrants(kindAgent, fakeGrants{{agent, uuid.New()}: {authz.ActionViewProject}}))

	tests := []struct {
		name      string
		principal auth.Principal
		resource  authz.Resource
		want      errs.Kind
	}{
		{"owner of another workspace", user(outsider), authz.Project(proj), errs.KindNotFound},
		{"project that does not exist", user(member), authz.Project(uuid.New()), errs.KindNotFound},
		{"unknown user", user(uuid.New()), authz.Project(proj), errs.KindNotFound},
		{"project paired with the wrong workspace", user(member), authz.Resource{WorkspaceID: other, ProjectID: proj}, errs.KindNotFound},
		{"agent without a grant for this project", auth.Principal{Kind: kindAgent, ID: agent}, authz.Project(proj), errs.KindNotFound},
		{"principal kind with no grants hook", auth.Principal{Kind: "runtime", ID: member}, authz.Project(proj), errs.KindNotFound},
		{"principal without id", auth.Principal{Kind: auth.KindUser}, authz.Project(proj), errs.KindUnauthenticated},
	}
	var msgs []string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, action := range []authz.Action{authz.ActionViewProject, authz.ActionDeleteProject} {
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

func TestCanProjectAcceptsMatchingWorkspace(t *testing.T) {
	ws, proj, uid := uuid.New(), uuid.New(), uuid.New()
	a := authz.New(fakeDirectory{
		fakeMemberships: fakeMemberships{{ws, uid}: authz.RoleDeveloper},
		projects:        map[uuid.UUID]fakeProject{proj: {ws, map[uuid.UUID]authz.ProjectRole{uid: authz.ProjectRoleViewer}}},
	})

	assertKind(t, a.Can(t.Context(), user(uid), authz.ActionViewProject, authz.Resource{WorkspaceID: ws, ProjectID: proj}), "")
}

func TestCanProjectAgentGrants(t *testing.T) {
	ws, proj, agent := uuid.New(), uuid.New(), uuid.New()
	dir := fakeDirectory{projects: map[uuid.UUID]fakeProject{proj: {workspace: ws}}}
	a := authz.New(dir, authz.WithGrants(kindAgent, fakeGrants{{agent, proj}: {authz.ActionViewProject, authz.ActionContribute}}))
	p := auth.Principal{Kind: kindAgent, ID: agent}

	tests := []struct {
		action   authz.Action
		resource authz.Resource
		want     errs.Kind
	}{
		{authz.ActionViewProject, authz.Project(proj), ""},
		{authz.ActionContribute, authz.Project(proj), ""},
		{authz.ActionDeleteProject, authz.Project(proj), errs.KindForbidden},
		{authz.ActionViewWorkspace, authz.Workspace(ws), errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			assertKind(t, a.Can(t.Context(), p, tt.action, tt.resource), tt.want)
		})
	}

	t.Run("agent is denied when no hook is registered", func(t *testing.T) {
		assertKind(t, authz.New(dir).Can(t.Context(), p, authz.ActionViewProject, authz.Project(proj)), errs.KindNotFound)
	})
}

func TestCanProjectDeniesByDefault(t *testing.T) {
	ws, proj, uid := uuid.New(), uuid.New(), uuid.New()
	newAuthz := func(proj uuid.UUID, wsRole authz.Role, projRole authz.ProjectRole) *authz.Authorizer {
		return authz.New(fakeDirectory{
			fakeMemberships: fakeMemberships{{ws, uid}: wsRole},
			projects:        map[uuid.UUID]fakeProject{proj: {ws, map[uuid.UUID]authz.ProjectRole{uid: projRole}}},
		})
	}

	t.Run("unknown action", func(t *testing.T) {
		a := newAuthz(proj, authz.RoleOwner, authz.ProjectRoleMaintainer)
		assertKind(t, a.Can(t.Context(), user(uid), "project.launch_missiles", authz.Project(proj)), errs.KindForbidden)
	})
	t.Run("unknown project role", func(t *testing.T) {
		a := newAuthz(proj, authz.RoleDeveloper, "superuser")
		assertKind(t, a.Can(t.Context(), user(uid), authz.ActionViewProject, authz.Project(proj)), errs.KindForbidden)
	})
}

func TestCanProjectSurfacesLookupFailuresAsInternal(t *testing.T) {
	boom := errors.New("connection refused")
	agent := uuid.New()

	tests := []struct {
		name string
		a    *authz.Authorizer
		p    auth.Principal
	}{
		{"membership lookup", authz.New(failingDirectory{failingMemberships{boom}}), user(uuid.New())},
		{"grant lookup", authz.New(fakeDirectory{}, authz.WithGrants(kindAgent, failingGrants{boom})), auth.Principal{Kind: kindAgent, ID: agent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.a.Can(t.Context(), tt.p, authz.ActionViewProject, authz.Project(uuid.New()))
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want it to wrap the lookup error", err)
			}
			if _, ok := errs.As(err); ok {
				t.Errorf("err = %v is a domain error; a failed lookup must become a 500, not a 403/404", err)
			}
		})
	}
}

func TestCanProjectWithoutProjectLookupIsInternal(t *testing.T) {
	a := authz.New(fakeMemberships{})

	err := a.Can(t.Context(), user(uuid.New()), authz.ActionViewProject, authz.Project(uuid.New()))

	if !errors.Is(err, authz.ErrNoProjectLookup) {
		t.Fatalf("err = %v, want ErrNoProjectLookup when the Memberships cannot look up projects", err)
	}
	if _, ok := errs.As(err); ok {
		t.Errorf("err = %v is a domain error; missing wiring is a server bug and must be a 500", err)
	}
}

type failingGrants struct{ err error }

func (f failingGrants) ProjectActions(context.Context, uuid.UUID, uuid.UUID) ([]authz.Action, error) {
	return nil, f.err
}

func TestNewWithProjectsChecksProjectsThroughTheDirectory(t *testing.T) {
	ws, proj, uid := uuid.New(), uuid.New(), uuid.New()
	dir := fakeDirectory{
		fakeMemberships: fakeMemberships{{ws, uid}: authz.RoleDeveloper},
		projects:        map[uuid.UUID]fakeProject{proj: {ws, map[uuid.UUID]authz.ProjectRole{uid: authz.ProjectRoleViewer}}},
	}
	a := authz.NewWithProjects(dir, authz.WithGrants(kindAgent, fakeGrants{}))

	assertKind(t, a.Can(t.Context(), user(uid), authz.ActionViewProject, authz.Project(proj)), "")
	assertKind(t, a.Can(t.Context(), user(uid), authz.ActionContribute, authz.Project(proj)), errs.KindForbidden)
	assertKind(t, a.Can(t.Context(), user(uid), authz.ActionViewWorkspace, authz.Workspace(ws)), "")
}
