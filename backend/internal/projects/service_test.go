package projects_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/projects"
)

// fakeRepo keeps projects and memberships in memory. It is also the
// authz.Memberships and authz.ProjectMemberships the real authorizer reads,
// so tests exercise the real role matrix.
type fakeRepo struct {
	mu        sync.Mutex
	rows      map[uuid.UUID]*projects.Project
	wsMembers map[uuid.UUID]map[uuid.UUID]authz.Role
	pMembers  map[uuid.UUID]map[uuid.UUID]authz.ProjectRole
	updates   int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		rows:      map[uuid.UUID]*projects.Project{},
		wsMembers: map[uuid.UUID]map[uuid.UUID]authz.Role{},
		pMembers:  map[uuid.UUID]map[uuid.UUID]authz.ProjectRole{},
	}
}

func (f *fakeRepo) nameTaken(workspaceID uuid.UUID, name string, except uuid.UUID) bool {
	for _, p := range f.rows {
		if p.WorkspaceID == workspaceID && p.Name == name && p.ID != except {
			return true
		}
	}
	return false
}

func (f *fakeRepo) Create(_ context.Context, n projects.NewProject) (projects.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nameTaken(n.WorkspaceID, n.Name, uuid.Nil) {
		return projects.Project{}, projects.ErrNameTaken
	}
	now := time.Now()
	p := &projects.Project{
		ID: uuid.New(), WorkspaceID: n.WorkspaceID, Name: n.Name, Description: n.Description,
		RepositoryURL: n.RepositoryURL, CreatedBy: n.CreatorID, CreatedAt: now, UpdatedAt: now,
	}
	f.rows[p.ID] = p
	f.pMembers[p.ID] = map[uuid.UUID]authz.ProjectRole{n.CreatorID: authz.ProjectRoleMaintainer}
	return *p, nil
}

func (f *fakeRepo) Get(_ context.Context, id uuid.UUID) (projects.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.rows[id]
	if !ok {
		return projects.Project{}, projects.ErrNotFound
	}
	return *p, nil
}

func (f *fakeRepo) List(_ context.Context, workspaceID, userID uuid.UUID, after string, limit int) ([]projects.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role := f.wsMembers[workspaceID][userID]
	var out []projects.Project
	for _, p := range f.rows {
		if p.WorkspaceID != workspaceID || p.Name <= after || role == "" {
			continue
		}
		if role == authz.RoleOwner || role == authz.RoleAdmin || f.pMembers[p.ID][userID] != "" {
			out = append(out, *p)
		}
	}
	slices.SortFunc(out, func(a, b projects.Project) int { return strings.Compare(a.Name, b.Name) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) Update(_ context.Context, id, _ uuid.UUID, in projects.UpdateInput) (projects.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.rows[id]
	if !ok {
		return projects.Project{}, projects.ErrNotFound
	}
	if in.Name != nil && f.nameTaken(p.WorkspaceID, *in.Name, id) {
		return projects.Project{}, projects.ErrNameTaken
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.RepositoryURL != nil {
		p.RepositoryURL = *in.RepositoryURL
	}
	f.updates++
	return *p, nil
}

func (f *fakeRepo) Role(_ context.Context, workspaceID, userID uuid.UUID) (authz.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.wsMembers[workspaceID][userID]
	if !ok {
		return "", authz.ErrNotMember
	}
	return role, nil
}

func (f *fakeRepo) ProjectMembership(_ context.Context, projectID, userID uuid.UUID) (authz.ProjectMembership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.rows[projectID]
	if !ok {
		return authz.ProjectMembership{}, authz.ErrNotMember
	}
	role, ok := f.wsMembers[p.WorkspaceID][userID]
	if !ok {
		return authz.ProjectMembership{}, authz.ErrNotMember
	}
	return authz.ProjectMembership{WorkspaceID: p.WorkspaceID, WorkspaceRole: role, ProjectRole: f.pMembers[projectID][userID]}, nil
}

func (f *fakeRepo) addWorkspaceMember(workspaceID, userID uuid.UUID, role authz.Role) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.wsMembers[workspaceID] == nil {
		f.wsMembers[workspaceID] = map[uuid.UUID]authz.Role{}
	}
	f.wsMembers[workspaceID][userID] = role
}

func (f *fakeRepo) addProjectMember(projectID, userID uuid.UUID, role authz.ProjectRole) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pMembers[projectID][userID] = role
}

type fixture struct {
	svc       *projects.Service
	repo      *fakeRepo
	workspace uuid.UUID
	owner     auth.Principal
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	repo := newFakeRepo()
	f := fixture{
		svc:       projects.NewService(repo, authz.New(repo)),
		repo:      repo,
		workspace: uuid.New(),
		owner:     newUser(),
	}
	repo.addWorkspaceMember(f.workspace, f.owner.ID, authz.RoleOwner)
	return f
}

func (f fixture) member(role authz.Role) auth.Principal {
	p := newUser()
	f.repo.addWorkspaceMember(f.workspace, p.ID, role)
	return p
}

func (f fixture) create(t *testing.T, name string) projects.Project {
	t.Helper()
	p, err := f.svc.Create(t.Context(), f.owner, f.workspace, projects.CreateInput{Name: name})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return p
}

func newUser() auth.Principal { return auth.Principal{Kind: auth.KindUser, ID: uuid.New()} }

func ptr(s string) *string { return &s }

func asKind(t *testing.T, err error, kind errs.Kind) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Kind != kind {
		t.Fatalf("err = %v, want kind %s", err, kind)
	}
	return e
}

func wantKind(t *testing.T, err error, kind errs.Kind) {
	t.Helper()
	if e, ok := errs.As(err); !ok || e.Kind != kind {
		t.Fatalf("err = %v, want kind %s", err, kind)
	}
}

func TestCreateByWorkspaceRole(t *testing.T) {
	tests := []struct {
		role authz.Role
		want errs.Kind
	}{
		{authz.RoleOwner, ""},
		{authz.RoleAdmin, ""},
		{authz.RoleProjectManager, ""},
		{authz.RoleDeveloper, errs.KindForbidden},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			f := newFixture(t)
			caller := f.member(tt.role)

			p, err := f.svc.Create(t.Context(), caller, f.workspace, projects.CreateInput{
				Name: "  Konduite ", Description: " The backend ", RepositoryURL: " https://github.com/acme/konduite.git ",
			})

			if tt.want != "" {
				wantKind(t, err, tt.want)
				if len(f.repo.rows) != 0 {
					t.Error("a project was stored for a caller without the role")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Name != "Konduite" || p.Description != "The backend" || p.RepositoryURL != "https://github.com/acme/konduite.git" ||
				p.WorkspaceID != f.workspace || p.CreatedBy != caller.ID {
				t.Errorf("project = %+v, want trimmed fields in the workspace, created by the caller", p)
			}
			if role := f.repo.pMembers[p.ID][caller.ID]; role != authz.ProjectRoleMaintainer {
				t.Errorf("creator's project role = %q, want maintainer", role)
			}
		})
	}
}

func TestCreateHidesWorkspacesFromNonMembers(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.Create(t.Context(), newUser(), f.workspace, projects.CreateInput{Name: "Konduite"})

	wantKind(t, err, errs.KindNotFound)
	if len(f.repo.rows) != 0 {
		t.Error("a non-member created a project")
	}
}

func TestCreateValidatesItsInput(t *testing.T) {
	tests := []struct {
		name  string
		in    projects.CreateInput
		field string
	}{
		{"blank name", projects.CreateInput{Name: "  "}, "name"},
		{"long name", projects.CreateInput{Name: strings.Repeat("a", 101)}, "name"},
		{"long description", projects.CreateInput{Name: "K", Description: strings.Repeat("a", 2001)}, "description"},
		{"http url", projects.CreateInput{Name: "K", RepositoryURL: "http://github.com/acme/k.git"}, "repository_url"},
		{"https with password", projects.CreateInput{Name: "K", RepositoryURL: "https://user:pass@github.com/acme/k.git"}, "repository_url"}, //nolint:gosec // fake credentials the validator must reject
		{"https with token", projects.CreateInput{Name: "K", RepositoryURL: "https://ghp_secret@github.com/acme/k.git"}, "repository_url"},
		{"ssh with password", projects.CreateInput{Name: "K", RepositoryURL: "ssh://git:pass@github.com/acme/k.git"}, "repository_url"}, //nolint:gosec // fake credentials the validator must reject
		{"scp-like with password", projects.CreateInput{Name: "K", RepositoryURL: "git:pass@github.com:acme/k.git"}, "repository_url"},
		{"no host", projects.CreateInput{Name: "K", RepositoryURL: "https:///acme/k.git"}, "repository_url"},
		{"no path", projects.CreateInput{Name: "K", RepositoryURL: "https://github.com/"}, "repository_url"},
		{"file url", projects.CreateInput{Name: "K", RepositoryURL: "file:///srv/k.git"}, "repository_url"},
		{"local path", projects.CreateInput{Name: "K", RepositoryURL: "/srv/k.git"}, "repository_url"},
		{"spaces", projects.CreateInput{Name: "K", RepositoryURL: "https://github.com/acme/my repo"}, "repository_url"},
		{"query string", projects.CreateInput{Name: "K", RepositoryURL: "https://github.com/acme/k.git?token=abc"}, "repository_url"},
		{"long url", projects.CreateInput{Name: "K", RepositoryURL: "https://github.com/" + strings.Repeat("a", 500)}, "repository_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.Create(t.Context(), f.owner, f.workspace, tt.in)
			if e := asKind(t, err, errs.KindInvalid); e.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tt.field)
			}
		})
	}
}

func TestCreateAcceptsGitURLs(t *testing.T) {
	for _, u := range []string{
		"https://github.com/acme/konduite",
		"https://github.com/acme/konduite.git",
		"https://gitlab.example.com:8443/group/sub/repo.git",
		"ssh://git@github.com/acme/konduite.git",
		"ssh://github.com:2222/acme/konduite.git",
		"git@github.com:acme/konduite.git",
		"",
	} {
		t.Run(u, func(t *testing.T) {
			f := newFixture(t)
			p, err := f.svc.Create(t.Context(), f.owner, f.workspace, projects.CreateInput{Name: "K", RepositoryURL: u})
			if err != nil {
				t.Fatal(err)
			}
			if p.RepositoryURL != u {
				t.Errorf("repository url = %q, want %q", p.RepositoryURL, u)
			}
		})
	}
}

func TestValidationProblemsDoNotEchoTheURL(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.Create(t.Context(), f.owner, f.workspace, projects.CreateInput{Name: "K", RepositoryURL: "https://me:hunter2@github.com/a/b"}) //nolint:gosec // fake credentials the validator must reject

	e := asKind(t, err, errs.KindInvalid)
	if strings.Contains(e.Error()+e.Fields["repository_url"], "hunter2") {
		t.Errorf("validation error %q / %q repeats the credential", e.Error(), e.Fields["repository_url"])
	}
}

func TestCreateWithATakenNameIsAConflict(t *testing.T) {
	f := newFixture(t)
	f.create(t, "Konduite")

	_, err := f.svc.Create(t.Context(), f.owner, f.workspace, projects.CreateInput{Name: "Konduite"})

	wantKind(t, err, errs.KindConflict)
}

func TestGetByStanding(t *testing.T) {
	tests := []struct {
		name        string
		role        authz.Role
		projectRole authz.ProjectRole
		want        errs.Kind
	}{
		{"owner", authz.RoleOwner, "", ""},
		{"admin", authz.RoleAdmin, "", ""},
		{"developer viewer", authz.RoleDeveloper, authz.ProjectRoleViewer, ""},
		{"project manager contributor", authz.RoleProjectManager, authz.ProjectRoleContributor, ""},
		{"developer without project role", authz.RoleDeveloper, "", errs.KindNotFound},
		{"project manager without project role", authz.RoleProjectManager, "", errs.KindNotFound},
		{"not a workspace member", "", "", errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.create(t, "Konduite")
			caller := newUser()
			if tt.role != "" {
				caller = f.member(tt.role)
			}
			if tt.projectRole != "" {
				f.repo.addProjectMember(p.ID, caller.ID, tt.projectRole)
			}

			got, err := f.svc.Get(t.Context(), caller, p.ID)

			if tt.want != "" {
				wantKind(t, err, tt.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != p.ID || got.Name != "Konduite" {
				t.Errorf("project = %+v, want Konduite", got)
			}
		})
	}
}

func TestEveryHiddenProjectGetsTheSameNotFound(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "Konduite")

	_, err := f.svc.Get(t.Context(), f.owner, uuid.New())
	missing := asKind(t, err, errs.KindNotFound)

	for name, caller := range map[string]auth.Principal{
		"non-member":            newUser(),
		"member without a role": f.member(authz.RoleDeveloper),
	} {
		_, err := f.svc.Get(t.Context(), caller, p.ID)
		if e := asKind(t, err, errs.KindNotFound); e.Message != missing.Message {
			t.Errorf("%s gets %q, a missing project %q; want the same", name, e.Message, missing.Message)
		}
		_, err = f.svc.Update(t.Context(), caller, p.ID, projects.UpdateInput{Name: ptr("Mine")})
		if e := asKind(t, err, errs.KindNotFound); e.Message != missing.Message {
			t.Errorf("%s updating gets %q, want %q", name, e.Message, missing.Message)
		}
	}
}

func TestListShowsOnlyProjectsTheCallerCanViewPageByPage(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"delta", "alpha", "charlie", "bravo"} {
		f.create(t, name)
	}
	other := newFixture(t)
	other.create(t, "elsewhere")

	first, err := f.svc.List(t.Context(), f.owner, f.workspace, projects.ListInput{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(first.Items); !slices.Equal(got, []string{"alpha", "bravo", "charlie"}) || first.Next == "" {
		t.Fatalf("first page = %v next %q, want alpha bravo charlie and a next position", got, first.Next)
	}
	second, err := f.svc.List(t.Context(), f.owner, f.workspace, projects.ListInput{After: first.Next, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(second.Items); !slices.Equal(got, []string{"delta"}) || second.Next != "" {
		t.Errorf("second page = %v next %q, want only delta and no next position", got, second.Next)
	}
}

func TestListLeavesOutProjectsAMemberHasNoRoleIn(t *testing.T) {
	f := newFixture(t)
	alpha := f.create(t, "alpha")
	f.create(t, "bravo")
	dev := f.member(authz.RoleDeveloper)
	f.repo.addProjectMember(alpha.ID, dev.ID, authz.ProjectRoleViewer)

	page, err := f.svc.List(t.Context(), dev, f.workspace, projects.ListInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(page.Items); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("developer sees %v, want only alpha", got)
	}
}

func TestListHidesWorkspacesFromNonMembers(t *testing.T) {
	f := newFixture(t)
	f.create(t, "alpha")

	_, err := f.svc.List(t.Context(), newUser(), f.workspace, projects.ListInput{Limit: 10})

	wantKind(t, err, errs.KindNotFound)
}

func TestListRejectsABadLimit(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(t.Context(), f.owner, f.workspace, projects.ListInput{Limit: 0})
	wantKind(t, err, errs.KindInvalid)
}

func TestUpdateByStanding(t *testing.T) {
	tests := []struct {
		name        string
		role        authz.Role
		projectRole authz.ProjectRole
		want        errs.Kind
	}{
		{"owner", authz.RoleOwner, "", ""},
		{"admin", authz.RoleAdmin, "", ""},
		{"maintainer", authz.RoleDeveloper, authz.ProjectRoleMaintainer, ""},
		{"contributor", authz.RoleDeveloper, authz.ProjectRoleContributor, errs.KindForbidden},
		{"viewer", authz.RoleProjectManager, authz.ProjectRoleViewer, errs.KindForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.create(t, "Konduite")
			caller := f.member(tt.role)
			if tt.projectRole != "" {
				f.repo.addProjectMember(p.ID, caller.ID, tt.projectRole)
			}

			got, err := f.svc.Update(t.Context(), caller, p.ID, projects.UpdateInput{
				Name: ptr(" Konduite API "), RepositoryURL: ptr(""),
			})

			if tt.want != "" {
				wantKind(t, err, tt.want)
				if f.repo.updates != 0 {
					t.Error("the project was updated by a caller without the role")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "Konduite API" || got.RepositoryURL != "" {
				t.Errorf("project = %+v, want renamed with no repository url", got)
			}
		})
	}
}

func TestUpdateValidatesItsInput(t *testing.T) {
	tests := []struct {
		name  string
		in    projects.UpdateInput
		field string
	}{
		{"nothing to change", projects.UpdateInput{}, ""},
		{"blank name", projects.UpdateInput{Name: ptr(" ")}, "name"},
		{"long description", projects.UpdateInput{Description: ptr(strings.Repeat("a", 2001))}, "description"},
		{"credentials in url", projects.UpdateInput{RepositoryURL: ptr("https://a:b@github.com/a/b")}, "repository_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.create(t, "Konduite")
			_, err := f.svc.Update(t.Context(), f.owner, p.ID, tt.in)
			e := asKind(t, err, errs.KindInvalid)
			if tt.field != "" && e.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tt.field)
			}
		})
	}
}

func TestUpdateToATakenNameIsAConflict(t *testing.T) {
	f := newFixture(t)
	f.create(t, "alpha")
	p := f.create(t, "bravo")

	_, err := f.svc.Update(t.Context(), f.owner, p.ID, projects.UpdateInput{Name: ptr("alpha")})

	wantKind(t, err, errs.KindConflict)
}

func TestRepositoryFailuresAreNotDomainErrors(t *testing.T) {
	boom := errors.New("connection reset")
	f := newFixture(t)
	svc := projects.NewService(failingRepo{boom}, authz.New(f.repo))

	_, err := svc.Create(t.Context(), f.owner, f.workspace, projects.CreateInput{Name: "Konduite"})

	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the repository error passed through", err)
	}
	if _, ok := errs.As(err); ok {
		t.Errorf("err = %v, want a plain error so httpx returns a generic 500", err)
	}
}

type failingRepo struct{ err error }

func (r failingRepo) Create(context.Context, projects.NewProject) (projects.Project, error) {
	return projects.Project{}, r.err
}

func (r failingRepo) Get(context.Context, uuid.UUID) (projects.Project, error) {
	return projects.Project{}, r.err
}

func (r failingRepo) List(context.Context, uuid.UUID, uuid.UUID, string, int) ([]projects.Project, error) {
	return nil, r.err
}

func (r failingRepo) Update(context.Context, uuid.UUID, uuid.UUID, projects.UpdateInput) (projects.Project, error) {
	return projects.Project{}, r.err
}

func names(ps []projects.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
