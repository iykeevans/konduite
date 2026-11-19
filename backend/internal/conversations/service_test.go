package conversations_test

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
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

type fakeRepo struct {
	mu       sync.Mutex
	rows     []conversations.Conversation
	projects map[uuid.UUID]uuid.UUID
	clock    time.Time
}

func (f *fakeRepo) Create(_ context.Context, n conversations.NewConversation) (conversations.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n.ProjectID != nil && f.projects[*n.ProjectID] != n.WorkspaceID {
		return conversations.Conversation{}, conversations.ErrProjectNotFound
	}
	f.clock = f.clock.Add(time.Second)
	c := conversations.Conversation{
		ID: uuid.New(), WorkspaceID: n.WorkspaceID, ProjectID: n.ProjectID, Title: n.Title, CreatedBy: n.CreatedBy,
		CreatedAt: f.clock, UpdatedAt: f.clock,
		Participants: []conversations.Participant{{Kind: conversations.KindUser, ID: n.CreatedBy, JoinedAt: f.clock}},
	}
	f.rows = append(f.rows, c)
	return c, nil
}

func (f *fakeRepo) Get(_ context.Context, id uuid.UUID) (conversations.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.rows {
		if c.ID == id {
			return c, nil
		}
	}
	return conversations.Conversation{}, conversations.ErrNotFound
}

func (f *fakeRepo) List(_ context.Context, q conversations.ListFilter) ([]conversations.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []conversations.Conversation
	for _, c := range slices.Backward(f.rows) {
		if c.WorkspaceID != q.WorkspaceID || (q.ProjectID != nil && (c.ProjectID == nil || *c.ProjectID != *q.ProjectID)) {
			continue
		}
		if q.After != nil && !c.CreatedAt.Before(q.After.CreatedAt) {
			continue
		}
		c.Participants = nil
		out = append(out, c)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

type fakeMemberships map[[2]uuid.UUID]authz.Role

func (f fakeMemberships) Role(_ context.Context, ws, user uuid.UUID) (authz.Role, error) {
	r, ok := f[[2]uuid.UUID{ws, user}]
	if !ok {
		return "", authz.ErrNotMember
	}
	return r, nil
}

type fixture struct {
	svc       *conversations.Service
	repo      *fakeRepo
	user      auth.Principal
	stranger  auth.Principal
	workspace uuid.UUID
	other     uuid.UUID
	project   uuid.UUID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{
		user:      auth.Principal{Kind: auth.KindUser, ID: uuid.New()},
		stranger:  auth.Principal{Kind: auth.KindUser, ID: uuid.New()},
		workspace: uuid.New(),
		other:     uuid.New(),
		project:   uuid.New(),
	}
	f.repo = &fakeRepo{projects: map[uuid.UUID]uuid.UUID{f.project: f.workspace}, clock: time.Unix(1_800_000_000, 0)}
	az := authz.New(fakeMemberships{
		{f.workspace, f.user.ID}: authz.RoleDeveloper,
		{f.other, f.stranger.ID}: authz.RoleOwner,
	})
	f.svc = conversations.NewService(f.repo, az)
	return f
}

func assertKind(t *testing.T, err error, want errs.Kind) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Kind != want {
		t.Fatalf("err = %v, want kind %s", err, want)
	}
}

func TestCreateAddsTheCreatorAsAParticipant(t *testing.T) {
	f := newFixture(t)

	c, err := f.svc.Create(t.Context(), f.user, conversations.CreateInput{
		WorkspaceID: f.workspace, ProjectID: &f.project, Title: "  Release planning  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkspaceID != f.workspace || c.ProjectID == nil || *c.ProjectID != f.project || c.Title != "Release planning" {
		t.Errorf("conversation = %+v, want trimmed title in the project", c)
	}
	if c.CreatedBy != f.user.ID || c.LastSeq != 0 {
		t.Errorf("created_by = %v, last_seq = %d, want the caller and 0", c.CreatedBy, c.LastSeq)
	}
	want := []conversations.Participant{{Kind: conversations.KindUser, ID: f.user.ID}}
	if len(c.Participants) != 1 || c.Participants[0].Kind != want[0].Kind || c.Participants[0].ID != want[0].ID {
		t.Errorf("participants = %+v, want only the creator", c.Participants)
	}
}

func TestCreateRejects(t *testing.T) {
	f := newFixture(t)
	foreignProject := uuid.New()
	f.repo.projects[foreignProject] = f.other
	tests := []struct {
		name   string
		caller auth.Principal
		in     conversations.CreateInput
		want   errs.Kind
		field  string
	}{
		{"no workspace", f.user, conversations.CreateInput{}, errs.KindInvalid, "workspace_id"},
		{"title too long", f.user, conversations.CreateInput{WorkspaceID: f.workspace, Title: strings.Repeat("é", 201)}, errs.KindInvalid, "title"},
		{"project in another workspace", f.user, conversations.CreateInput{WorkspaceID: f.workspace, ProjectID: &foreignProject}, errs.KindInvalid, "project_id"},
		{"unknown project", f.user, conversations.CreateInput{WorkspaceID: f.workspace, ProjectID: new(uuid.New())}, errs.KindInvalid, "project_id"},
		{"non-member", f.stranger, conversations.CreateInput{WorkspaceID: f.workspace}, errs.KindNotFound, ""},
		{"no principal", auth.Principal{}, conversations.CreateInput{WorkspaceID: f.workspace}, errs.KindUnauthenticated, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Create(t.Context(), tt.caller, tt.in)
			assertKind(t, err, tt.want)
			if e, _ := errs.As(err); tt.field != "" && e.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tt.field)
			}
		})
	}
	if len(f.repo.rows) != 0 {
		t.Errorf("%d conversations stored, want 0", len(f.repo.rows))
	}
}

func TestCreateChecksMembershipBeforeTheProject(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Create(t.Context(), f.stranger, conversations.CreateInput{WorkspaceID: f.workspace, ProjectID: new(uuid.New())})
	assertKind(t, err, errs.KindNotFound)
}

func TestGetReturnsTheConversationWithItsParticipants(t *testing.T) {
	f := newFixture(t)
	created, err := f.svc.Create(t.Context(), f.user, conversations.CreateInput{WorkspaceID: f.workspace})
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.Get(t.Context(), f.user, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || len(got.Participants) != 1 {
		t.Errorf("got %+v, want %v with its creator", got, created.ID)
	}
}

func TestGetHidesConversationsOutsideTheCallersWorkspaces(t *testing.T) {
	f := newFixture(t)
	created, err := f.svc.Create(t.Context(), f.user, conversations.CreateInput{WorkspaceID: f.workspace})
	if err != nil {
		t.Fatal(err)
	}

	_, hidden := f.svc.Get(t.Context(), f.stranger, created.ID)
	_, missing := f.svc.Get(t.Context(), f.stranger, uuid.New())

	assertKind(t, hidden, errs.KindNotFound)
	assertKind(t, missing, errs.KindNotFound)
	if hidden.Error() != missing.Error() {
		t.Errorf("hidden = %q, missing = %q: a non-member can tell them apart", hidden, missing)
	}
}

func TestListPagesNewestFirst(t *testing.T) {
	f := newFixture(t)
	var ids []uuid.UUID
	for range 5 {
		c, err := f.svc.Create(t.Context(), f.user, conversations.CreateInput{WorkspaceID: f.workspace})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	if _, err := f.svc.Create(t.Context(), f.stranger, conversations.CreateInput{WorkspaceID: f.other}); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	var after *conversations.Position
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("pagination did not end")
		}
		page, err := f.svc.List(t.Context(), f.user, conversations.ListInput{WorkspaceID: f.workspace, Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Items {
			seen = append(seen, c.ID)
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	slices.Reverse(ids)
	if !slices.Equal(seen, ids) {
		t.Errorf("listed %v, want %v", seen, ids)
	}
}

func TestListFiltersByProject(t *testing.T) {
	f := newFixture(t)
	inProject, err := f.svc.Create(t.Context(), f.user, conversations.CreateInput{WorkspaceID: f.workspace, ProjectID: &f.project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(t.Context(), f.user, conversations.CreateInput{WorkspaceID: f.workspace}); err != nil {
		t.Fatal(err)
	}

	page, err := f.svc.List(t.Context(), f.user, conversations.ListInput{WorkspaceID: f.workspace, ProjectID: &f.project, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != inProject.ID || page.Next != nil {
		t.Errorf("page = %+v, want only %v", page, inProject.ID)
	}
}

func TestListRejects(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(t.Context(), f.stranger, conversations.ListInput{WorkspaceID: f.workspace, Limit: 10})
	assertKind(t, err, errs.KindNotFound)

	_, err = f.svc.List(t.Context(), f.user, conversations.ListInput{Limit: 10})
	assertKind(t, err, errs.KindInvalid)
}

func TestGetPassesLookupFailuresThrough(t *testing.T) {
	boom := errors.New("connection reset")
	svc := conversations.NewService(failingRepo{boom}, authz.New(fakeMemberships{}))
	if _, err := svc.Get(t.Context(), auth.Principal{Kind: auth.KindUser, ID: uuid.New()}, uuid.New()); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

type failingRepo struct{ err error }

func (r failingRepo) Create(context.Context, conversations.NewConversation) (conversations.Conversation, error) {
	return conversations.Conversation{}, r.err
}
func (r failingRepo) Get(context.Context, uuid.UUID) (conversations.Conversation, error) {
	return conversations.Conversation{}, r.err
}
func (r failingRepo) List(context.Context, conversations.ListFilter) ([]conversations.Conversation, error) {
	return nil, r.err
}
