package workspaces_test

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
	"github.com/iykeevans/konduite/backend/internal/workspaces"
)

type stored struct {
	id                   uuid.UUID
	name, slug           string
	createdBy            uuid.UUID
	createdAt, updatedAt time.Time
}

// fakeRepo keeps workspaces and memberships in memory. It is also the
// authz.Memberships the real authorizer reads, so tests exercise the real
// role matrix.
type fakeRepo struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]*stored
	members map[uuid.UUID]map[uuid.UUID]authz.Role
	updates int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rows: map[uuid.UUID]*stored{}, members: map[uuid.UUID]map[uuid.UUID]authz.Role{}}
}

func (f *fakeRepo) slugTaken(slug string, except uuid.UUID) bool {
	for _, s := range f.rows {
		if s.slug == slug && s.id != except {
			return true
		}
	}
	return false
}

func (f *fakeRepo) view(s *stored, user uuid.UUID) workspaces.Workspace {
	return workspaces.Workspace{
		ID: s.id, Name: s.name, Slug: s.slug, Role: f.members[s.id][user],
		CreatedBy: s.createdBy, CreatedAt: s.createdAt, UpdatedAt: s.updatedAt,
	}
}

func (f *fakeRepo) Create(_ context.Context, n workspaces.NewWorkspace) (workspaces.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.slugTaken(n.Slug, uuid.Nil) {
		return workspaces.Workspace{}, workspaces.ErrSlugTaken
	}
	now := time.Now()
	s := &stored{id: uuid.New(), name: n.Name, slug: n.Slug, createdBy: n.OwnerID, createdAt: now, updatedAt: now}
	f.rows[s.id] = s
	f.members[s.id] = map[uuid.UUID]authz.Role{n.OwnerID: authz.RoleOwner}
	return f.view(s, n.OwnerID), nil
}

func (f *fakeRepo) Get(_ context.Context, id, user uuid.UUID) (workspaces.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.rows[id]
	if !ok || f.members[id][user] == "" {
		return workspaces.Workspace{}, workspaces.ErrNotFound
	}
	return f.view(s, user), nil
}

func (f *fakeRepo) List(_ context.Context, user uuid.UUID, after string, limit int) ([]workspaces.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workspaces.Workspace
	for _, s := range f.rows {
		if f.members[s.id][user] != "" && s.slug > after {
			out = append(out, f.view(s, user))
		}
	}
	slices.SortFunc(out, func(a, b workspaces.Workspace) int { return strings.Compare(a.Slug, b.Slug) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) Update(_ context.Context, id, actor uuid.UUID, in workspaces.UpdateInput) (workspaces.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.rows[id]
	if !ok {
		return workspaces.Workspace{}, workspaces.ErrNotFound
	}
	if in.Slug != nil && f.slugTaken(*in.Slug, id) {
		return workspaces.Workspace{}, workspaces.ErrSlugTaken
	}
	if in.Name != nil {
		s.name = *in.Name
	}
	if in.Slug != nil {
		s.slug = *in.Slug
	}
	f.updates++
	return f.view(s, actor), nil
}

func (f *fakeRepo) Role(_ context.Context, workspaceID, userID uuid.UUID) (authz.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.members[workspaceID][userID]
	if !ok {
		return "", authz.ErrNotMember
	}
	return role, nil
}

func (f *fakeRepo) addMember(workspaceID, userID uuid.UUID, role authz.Role) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[workspaceID][userID] = role
}

type fixture struct {
	svc  *workspaces.Service
	repo *fakeRepo
	user auth.Principal
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	repo := newFakeRepo()
	return fixture{
		svc:  workspaces.NewService(repo, authz.New(repo)),
		repo: repo,
		user: newUser(),
	}
}

func newUser() auth.Principal { return auth.Principal{Kind: auth.KindUser, ID: uuid.New()} }

func ptr(s string) *string { return &s }

func (f fixture) create(t *testing.T, name, slug string) workspaces.Workspace {
	t.Helper()
	ws, err := f.svc.Create(t.Context(), f.user, workspaces.CreateInput{Name: name, Slug: slug})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return ws
}

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

// Creating a workspace has no workspace to check yet, so it calls no
// authz.Can resource check: any signed-in user may create one and becomes
// its owner.
func TestAnySignedInUserCreatesAWorkspaceAndBecomesItsOwner(t *testing.T) {
	f := newFixture(t)

	ws, err := f.svc.Create(t.Context(), f.user, workspaces.CreateInput{Name: "  Acme Robotics ", Slug: " acme-robotics "})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Name != "Acme Robotics" || ws.Slug != "acme-robotics" || ws.CreatedBy != f.user.ID || ws.Role != authz.RoleOwner {
		t.Errorf("workspace = %+v, want trimmed name and slug, created by and owned by the caller", ws)
	}
	if role, _ := f.repo.Role(t.Context(), ws.ID, f.user.ID); role != authz.RoleOwner {
		t.Errorf("stored role = %q, want owner", role)
	}
}

func TestCreateRejectsCallersWhoAreNotSignedInUsers(t *testing.T) {
	f := newFixture(t)
	in := workspaces.CreateInput{Name: "Acme", Slug: "acme"}

	_, err := f.svc.Create(t.Context(), auth.Principal{}, in)
	wantKind(t, err, errs.KindUnauthenticated)

	_, err = f.svc.Create(t.Context(), auth.Principal{Kind: "runtime", ID: uuid.New()}, in)
	wantKind(t, err, errs.KindForbidden)

	if len(f.repo.rows) != 0 {
		t.Error("a workspace was stored for a rejected caller")
	}
}

func TestCreateValidatesNameAndSlug(t *testing.T) {
	tests := []struct {
		name  string
		in    workspaces.CreateInput
		field string
	}{
		{"blank name", workspaces.CreateInput{Name: "  ", Slug: "acme"}, "name"},
		{"long name", workspaces.CreateInput{Name: strings.Repeat("a", 101), Slug: "acme"}, "name"},
		{"missing slug", workspaces.CreateInput{Name: "Acme"}, "slug"},
		{"uppercase slug", workspaces.CreateInput{Name: "Acme", Slug: "Acme"}, "slug"},
		{"slug with spaces", workspaces.CreateInput{Name: "Acme", Slug: "acme corp"}, "slug"},
		{"leading hyphen", workspaces.CreateInput{Name: "Acme", Slug: "-acme"}, "slug"},
		{"double hyphen", workspaces.CreateInput{Name: "Acme", Slug: "acme--corp"}, "slug"},
		{"too short", workspaces.CreateInput{Name: "Acme", Slug: "ac"}, "slug"},
		{"too long", workspaces.CreateInput{Name: "Acme", Slug: strings.Repeat("a", 51)}, "slug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.Create(t.Context(), f.user, tt.in)
			if e := asKind(t, err, errs.KindInvalid); e.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tt.field)
			}
		})
	}
}

func TestCreateWithATakenSlugIsAConflict(t *testing.T) {
	f := newFixture(t)
	f.create(t, "Acme", "acme")

	_, err := f.svc.Create(t.Context(), newUser(), workspaces.CreateInput{Name: "Other Acme", Slug: "acme"})

	wantKind(t, err, errs.KindConflict)
}

func TestMemberGetsAWorkspaceWithTheirRole(t *testing.T) {
	f := newFixture(t)
	ws := f.create(t, "Acme", "acme")
	dev := newUser()
	f.repo.addMember(ws.ID, dev.ID, authz.RoleDeveloper)

	got, err := f.svc.Get(t.Context(), dev, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ws.ID || got.Slug != "acme" || got.Role != authz.RoleDeveloper {
		t.Errorf("workspace = %+v, want acme seen as a developer", got)
	}
}

func TestGetHidesWorkspacesFromNonMembers(t *testing.T) {
	f := newFixture(t)
	ws := f.create(t, "Acme", "acme")

	_, err := f.svc.Get(t.Context(), newUser(), ws.ID)
	missing := asKind(t, err, errs.KindNotFound)

	_, err = f.svc.Get(t.Context(), f.user, uuid.New())
	if e := asKind(t, err, errs.KindNotFound); e.Message != missing.Message {
		t.Errorf("missing workspace says %q, non-member says %q; want the same", e.Message, missing.Message)
	}
}

func TestListReturnsOnlyTheCallersWorkspacesPageByPage(t *testing.T) {
	f := newFixture(t)
	for _, slug := range []string{"delta", "alpha", "charlie", "bravo"} {
		f.create(t, strings.ToUpper(slug), slug)
	}
	stranger := fixture{svc: f.svc, repo: f.repo, user: newUser()}
	stranger.create(t, "Secret", "secret")

	first, err := f.svc.List(t.Context(), f.user, workspaces.ListInput{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := slugs(first.Items); !slices.Equal(got, []string{"alpha", "bravo", "charlie"}) || first.Next == "" {
		t.Fatalf("first page = %v next %q, want alpha bravo charlie and a next position", got, first.Next)
	}
	second, err := f.svc.List(t.Context(), f.user, workspaces.ListInput{After: first.Next, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := slugs(second.Items); !slices.Equal(got, []string{"delta"}) || second.Next != "" {
		t.Errorf("second page = %v next %q, want only delta and no next position", got, second.Next)
	}
}

func TestListOfAnExactlyFullPageHasNoNext(t *testing.T) {
	f := newFixture(t)
	f.create(t, "Alpha", "alpha")
	f.create(t, "Bravo", "bravo")

	page, err := f.svc.List(t.Context(), f.user, workspaces.ListInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Next != "" {
		t.Errorf("page = %v next %q, want both workspaces and no next position", slugs(page.Items), page.Next)
	}
}

func TestListRejectsCallersWhoAreNotSignedInUsers(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(t.Context(), auth.Principal{}, workspaces.ListInput{Limit: 10})
	wantKind(t, err, errs.KindUnauthenticated)
}

func TestUpdateByRole(t *testing.T) {
	tests := []struct {
		role authz.Role
		want errs.Kind
	}{
		{authz.RoleOwner, ""},
		{authz.RoleAdmin, ""},
		{authz.RoleDeveloper, errs.KindForbidden},
		{authz.RoleProjectManager, errs.KindForbidden},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			f := newFixture(t)
			ws := f.create(t, "Acme", "acme")
			caller := newUser()
			if tt.role == authz.RoleOwner {
				caller = f.user
			} else {
				f.repo.addMember(ws.ID, caller.ID, tt.role)
			}

			got, err := f.svc.Update(t.Context(), caller, ws.ID, workspaces.UpdateInput{Name: ptr(" Acme Inc ")})

			if tt.want != "" {
				wantKind(t, err, tt.want)
				if f.repo.updates != 0 {
					t.Error("the workspace was updated by a caller without the role")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "Acme Inc" || got.Slug != "acme" || got.Role != tt.role {
				t.Errorf("workspace = %+v, want renamed to Acme Inc, slug unchanged, seen as %s", got, tt.role)
			}
		})
	}
}

func TestUpdateHidesWorkspacesFromNonMembers(t *testing.T) {
	f := newFixture(t)
	ws := f.create(t, "Acme", "acme")

	_, err := f.svc.Update(t.Context(), newUser(), ws.ID, workspaces.UpdateInput{Name: ptr("Mine now")})

	wantKind(t, err, errs.KindNotFound)
	if f.repo.updates != 0 {
		t.Error("a non-member updated the workspace")
	}
}

func TestUpdateValidatesItsInput(t *testing.T) {
	tests := []struct {
		name  string
		in    workspaces.UpdateInput
		field string
	}{
		{"nothing to change", workspaces.UpdateInput{}, ""},
		{"blank name", workspaces.UpdateInput{Name: ptr(" ")}, "name"},
		{"bad slug", workspaces.UpdateInput{Slug: ptr("Not A Slug")}, "slug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ws := f.create(t, "Acme", "acme")
			_, err := f.svc.Update(t.Context(), f.user, ws.ID, tt.in)
			e := asKind(t, err, errs.KindInvalid)
			if tt.field != "" && e.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tt.field)
			}
		})
	}
}

func TestUpdateToATakenSlugIsAConflict(t *testing.T) {
	f := newFixture(t)
	f.create(t, "Acme", "acme")
	ws := f.create(t, "Bolt", "bolt")

	_, err := f.svc.Update(t.Context(), f.user, ws.ID, workspaces.UpdateInput{Slug: ptr("acme")})

	wantKind(t, err, errs.KindConflict)
}

func TestRepositoryFailuresAreNotDomainErrors(t *testing.T) {
	boom := errors.New("connection reset")
	svc := workspaces.NewService(failingRepo{boom}, authz.New(newFakeRepo()))

	_, err := svc.Create(t.Context(), newUser(), workspaces.CreateInput{Name: "Acme", Slug: "acme"})

	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the repository error passed through", err)
	}
	if _, ok := errs.As(err); ok {
		t.Errorf("err = %v, want a plain error so httpx returns a generic 500", err)
	}
}

type failingRepo struct{ err error }

func (r failingRepo) Create(context.Context, workspaces.NewWorkspace) (workspaces.Workspace, error) {
	return workspaces.Workspace{}, r.err
}

func (r failingRepo) Get(context.Context, uuid.UUID, uuid.UUID) (workspaces.Workspace, error) {
	return workspaces.Workspace{}, r.err
}

func (r failingRepo) List(context.Context, uuid.UUID, string, int) ([]workspaces.Workspace, error) {
	return nil, r.err
}

func (r failingRepo) Update(context.Context, uuid.UUID, uuid.UUID, workspaces.UpdateInput) (workspaces.Workspace, error) {
	return workspaces.Workspace{}, r.err
}

func slugs(ws []workspaces.Workspace) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Slug
	}
	return out
}
