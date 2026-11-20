package projectmembers_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/projectmembers"
)

// fakeRepo keeps one workspace's members and its projects' members in
// memory. It is also the authz.Directory the real authorizer reads, so tests
// exercise the real role matrix, and it applies CheckMaintainerChange the
// way the Postgres repository does.
type fakeRepo struct {
	mu        sync.Mutex
	workspace uuid.UUID
	projects  map[uuid.UUID]bool
	wsMembers map[uuid.UUID]authz.Role
	emails    map[uuid.UUID]string
	pMembers  map[uuid.UUID]map[uuid.UUID]projectmembers.Member
	clock     time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		workspace: uuid.New(),
		projects:  map[uuid.UUID]bool{},
		wsMembers: map[uuid.UUID]authz.Role{},
		emails:    map[uuid.UUID]string{},
		pMembers:  map[uuid.UUID]map[uuid.UUID]projectmembers.Member{},
		clock:     time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
	}
}

func (f *fakeRepo) put(projectID, userID uuid.UUID, role authz.ProjectRole) projectmembers.Member {
	f.clock = f.clock.Add(time.Second)
	m := projectmembers.Member{
		UserID: userID, Role: role, Email: f.emails[userID],
		WorkspaceRole: f.wsMembers[userID], JoinedAt: f.clock,
	}
	f.pMembers[projectID][userID] = m
	return m
}

func (f *fakeRepo) List(_ context.Context, projectID uuid.UUID, after projectmembers.Cursor, limit int) ([]projectmembers.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []projectmembers.Member
	for _, m := range f.pMembers[projectID] {
		if _, inWorkspace := f.wsMembers[m.UserID]; !inWorkspace {
			continue
		}
		if m.JoinedAt.After(after.JoinedAt) || (m.JoinedAt.Equal(after.JoinedAt) && m.UserID.String() > after.UserID.String()) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b projectmembers.Member) int { return a.JoinedAt.Compare(b.JoinedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) Add(_ context.Context, c projectmembers.Change) (projectmembers.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.wsMembers[c.UserID]; !ok {
		return projectmembers.Member{}, projectmembers.ErrNotWorkspaceMember
	}
	if _, ok := f.pMembers[c.ProjectID][c.UserID]; ok {
		return projectmembers.Member{}, projectmembers.ErrAlreadyMember
	}
	return f.put(c.ProjectID, c.UserID, c.Role), nil
}

func (f *fakeRepo) check(c projectmembers.Change, next authz.ProjectRole) (projectmembers.Member, error) {
	target, ok := f.pMembers[c.ProjectID][c.UserID]
	if !ok {
		return projectmembers.Member{}, projectmembers.ErrMemberNotFound
	}
	maintainers := 0
	for _, m := range f.pMembers[c.ProjectID] {
		if m.Role == authz.ProjectRoleMaintainer {
			maintainers++
		}
	}
	implicit := slices.Contains(authz.ImplicitMaintainers(), f.wsMembers[c.ActorID])
	return target, projectmembers.CheckMaintainerChange(implicit, target.Role, next, maintainers)
}

func (f *fakeRepo) ChangeRole(_ context.Context, c projectmembers.Change) (projectmembers.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target, err := f.check(c, c.Role)
	if err != nil {
		return projectmembers.Member{}, err
	}
	target.Role = c.Role
	f.pMembers[c.ProjectID][c.UserID] = target
	return target, nil
}

func (f *fakeRepo) Remove(_ context.Context, c projectmembers.Change) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.check(c, ""); err != nil {
		return err
	}
	delete(f.pMembers[c.ProjectID], c.UserID)
	return nil
}

func (f *fakeRepo) Role(_ context.Context, workspaceID, userID uuid.UUID) (authz.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.wsMembers[userID]
	if workspaceID != f.workspace || !ok {
		return "", authz.ErrNotMember
	}
	return role, nil
}

func (f *fakeRepo) ProjectMembership(_ context.Context, projectID, userID uuid.UUID) (authz.ProjectMembership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.wsMembers[userID]
	if !f.projects[projectID] || !ok {
		return authz.ProjectMembership{}, authz.ErrNotMember
	}
	return authz.ProjectMembership{
		WorkspaceID: f.workspace, WorkspaceRole: role, ProjectRole: f.pMembers[projectID][userID].Role,
	}, nil
}

type fixture struct {
	repo    *fakeRepo
	svc     *projectmembers.Service
	project uuid.UUID

	owner, admin, maintainer, contributor, viewer, bystander, outsider uuid.UUID
}

// newFixture makes a project with one maintainer, one contributor and one
// viewer. owner and admin have no project row; bystander is a developer in
// the workspace with no project row; outsider is in no workspace.
func newFixture(t *testing.T) fixture {
	t.Helper()
	repo := newFakeRepo()
	f := fixture{
		repo: repo, svc: projectmembers.NewService(repo, authz.NewWithProjects(repo)), project: uuid.New(),
		owner: uuid.New(), admin: uuid.New(), maintainer: uuid.New(), contributor: uuid.New(),
		viewer: uuid.New(), bystander: uuid.New(), outsider: uuid.New(),
	}
	repo.projects[f.project] = true
	repo.pMembers[f.project] = map[uuid.UUID]projectmembers.Member{}
	for id, role := range map[uuid.UUID]authz.Role{
		f.owner: authz.RoleOwner, f.admin: authz.RoleAdmin, f.maintainer: authz.RoleDeveloper,
		f.contributor: authz.RoleDeveloper, f.viewer: authz.RoleProjectManager, f.bystander: authz.RoleDeveloper,
	} {
		repo.wsMembers[id] = role
		repo.emails[id] = id.String()[:8] + "@example.com"
	}
	repo.put(f.project, f.maintainer, authz.ProjectRoleMaintainer)
	repo.put(f.project, f.contributor, authz.ProjectRoleContributor)
	repo.put(f.project, f.viewer, authz.ProjectRoleViewer)
	return f
}

func user(id uuid.UUID) auth.Principal { return auth.Principal{Kind: auth.KindUser, ID: id} }

func wantKind(t *testing.T, err error, want errs.Kind) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		return
	}
	e, ok := errs.As(err)
	if !ok || e.Kind != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
}

func TestListIsOpenToEveryoneWhoCanSeeTheProject(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		caller uuid.UUID
		want   errs.Kind
	}{
		{"maintainer", f.maintainer, ""},
		{"contributor", f.contributor, ""},
		{"viewer", f.viewer, ""},
		{"workspace owner without a project role", f.owner, ""},
		{"workspace admin without a project role", f.admin, ""},
		{"workspace member without a project role", f.bystander, errs.KindNotFound},
		{"outsider", f.outsider, errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := f.svc.List(t.Context(), user(tt.caller), f.project, projectmembers.Cursor{}, 50)
			wantKind(t, err, tt.want)
			if tt.want == "" && len(page.Items) != 3 {
				t.Errorf("%d members, want 3", len(page.Items))
			}
		})
	}
}

func TestListOfAMissingProjectIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(t.Context(), user(f.owner), uuid.New(), projectmembers.Cursor{}, 50)
	wantKind(t, err, errs.KindNotFound)
}

func TestListPagesInJoinOrder(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.List(t.Context(), user(f.viewer), f.project, projectmembers.Cursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Next == nil || first.Items[0].UserID != f.maintainer {
		t.Fatalf("first page = %+v, want maintainer and contributor with a next cursor", first)
	}
	second, err := f.svc.List(t.Context(), user(f.viewer), f.project, *first.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Next != nil || second.Items[0].UserID != f.viewer {
		t.Fatalf("second page = %+v, want only the viewer and no cursor", second)
	}
}

func TestListRejectsAZeroLimit(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(t.Context(), user(f.viewer), f.project, projectmembers.Cursor{}, 0)
	wantKind(t, err, errs.KindInvalid)
}

func TestAdd(t *testing.T) {
	tests := []struct {
		name   string
		caller func(fixture) uuid.UUID
		target func(fixture) uuid.UUID
		role   string
		want   errs.Kind
	}{
		{"maintainer adds a workspace member", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.bystander }, "contributor", ""},
		{"workspace owner adds a workspace member", func(f fixture) uuid.UUID { return f.owner }, func(f fixture) uuid.UUID { return f.bystander }, "maintainer", ""},
		{"workspace admin adds themselves", func(f fixture) uuid.UUID { return f.admin }, func(f fixture) uuid.UUID { return f.admin }, "viewer", ""},
		{"role is trimmed", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.bystander }, " viewer ", ""},
		{"contributor cannot add", func(f fixture) uuid.UUID { return f.contributor }, func(f fixture) uuid.UUID { return f.bystander }, "viewer", errs.KindForbidden},
		{"viewer cannot add", func(f fixture) uuid.UUID { return f.viewer }, func(f fixture) uuid.UUID { return f.bystander }, "viewer", errs.KindForbidden},
		{"workspace member without a project role gets 404", func(f fixture) uuid.UUID { return f.bystander }, func(f fixture) uuid.UUID { return f.bystander }, "viewer", errs.KindNotFound},
		{"outsider gets 404", func(f fixture) uuid.UUID { return f.outsider }, func(f fixture) uuid.UUID { return f.bystander }, "viewer", errs.KindNotFound},
		{"someone outside the workspace cannot be added", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.outsider }, "viewer", errs.KindInvalid},
		{"an existing member cannot be added again", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.viewer }, "contributor", errs.KindConflict},
		{"unknown role", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.bystander }, "owner", errs.KindInvalid},
		{"missing user", func(f fixture) uuid.UUID { return f.maintainer }, func(fixture) uuid.UUID { return uuid.Nil }, "viewer", errs.KindInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			target := tt.target(f)
			m, err := f.svc.Add(t.Context(), user(tt.caller(f)), f.project, target, tt.role)
			wantKind(t, err, tt.want)
			if tt.want != "" {
				return
			}
			if m.UserID != target || m.Email == "" || m.Role == "" || m.WorkspaceRole == "" {
				t.Errorf("member = %+v, want the added user with email and roles", m)
			}
		})
	}
}

func TestAddValidatesBeforeRevealingTheProject(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Add(t.Context(), user(f.outsider), f.project, f.bystander, "nope")
	wantKind(t, err, errs.KindInvalid)
}

func TestChangeRole(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(fixture)
		caller func(fixture) uuid.UUID
		target func(fixture) uuid.UUID
		role   string
		want   errs.Kind
	}{
		{"maintainer promotes a contributor", nil, func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.contributor }, "maintainer", ""},
		{"maintainer demotes a viewer's peer", nil, func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.contributor }, "viewer", ""},
		{"last maintainer cannot demote themselves", nil, func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.maintainer }, "contributor", errs.KindConflict},
		{"a maintainer can step down when another remains",
			func(f fixture) { f.repo.put(f.project, f.bystander, authz.ProjectRoleMaintainer) },
			func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.maintainer }, "viewer", ""},
		{"workspace admin demotes the last maintainer", nil, func(f fixture) uuid.UUID { return f.admin }, func(f fixture) uuid.UUID { return f.maintainer }, "viewer", ""},
		{"keeping the last maintainer's role is a no-op", nil, func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.maintainer }, "maintainer", ""},
		{"contributor cannot change roles", nil, func(f fixture) uuid.UUID { return f.contributor }, func(f fixture) uuid.UUID { return f.viewer }, "contributor", errs.KindForbidden},
		{"workspace member without a project role gets 404", nil, func(f fixture) uuid.UUID { return f.bystander }, func(f fixture) uuid.UUID { return f.viewer }, "contributor", errs.KindNotFound},
		{"someone with no project role is not a member", nil, func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.bystander }, "viewer", errs.KindNotFound},
		{"unknown role", nil, func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.viewer }, "admin", errs.KindInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			m, err := f.svc.ChangeRole(t.Context(), user(tt.caller(f)), f.project, tt.target(f), tt.role)
			wantKind(t, err, tt.want)
			if tt.want == "" && string(m.Role) != tt.role {
				t.Errorf("role = %q, want %q", m.Role, tt.role)
			}
		})
	}
}

func TestRemove(t *testing.T) {
	tests := []struct {
		name   string
		caller func(fixture) uuid.UUID
		target func(fixture) uuid.UUID
		want   errs.Kind
	}{
		{"maintainer removes a contributor", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.contributor }, ""},
		{"contributor leaves", func(f fixture) uuid.UUID { return f.contributor }, func(f fixture) uuid.UUID { return f.contributor }, ""},
		{"viewer leaves", func(f fixture) uuid.UUID { return f.viewer }, func(f fixture) uuid.UUID { return f.viewer }, ""},
		{"last maintainer cannot leave", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.maintainer }, errs.KindConflict},
		{"workspace owner removes the last maintainer", func(f fixture) uuid.UUID { return f.owner }, func(f fixture) uuid.UUID { return f.maintainer }, ""},
		{"contributor cannot remove others", func(f fixture) uuid.UUID { return f.contributor }, func(f fixture) uuid.UUID { return f.viewer }, errs.KindForbidden},
		{"viewer cannot remove others", func(f fixture) uuid.UUID { return f.viewer }, func(f fixture) uuid.UUID { return f.contributor }, errs.KindForbidden},
		{"workspace member without a project role gets 404", func(f fixture) uuid.UUID { return f.bystander }, func(f fixture) uuid.UUID { return f.viewer }, errs.KindNotFound},
		{"leaving a project you are not in is 404", func(f fixture) uuid.UUID { return f.bystander }, func(f fixture) uuid.UUID { return f.bystander }, errs.KindNotFound},
		{"outsider gets 404", func(f fixture) uuid.UUID { return f.outsider }, func(f fixture) uuid.UUID { return f.viewer }, errs.KindNotFound},
		{"removing a non-member is 404", func(f fixture) uuid.UUID { return f.maintainer }, func(f fixture) uuid.UUID { return f.bystander }, errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			target := tt.target(f)
			err := f.svc.Remove(t.Context(), user(tt.caller(f)), f.project, target)
			wantKind(t, err, tt.want)
			_, stillThere := f.repo.pMembers[f.project][target]
			if tt.want == "" && stillThere {
				t.Error("member is still in the project")
			}
		})
	}
}
