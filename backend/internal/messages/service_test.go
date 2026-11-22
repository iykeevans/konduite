package messages_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/messages"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

type fakeConversationRepo struct {
	conversations.Repository
	rows map[uuid.UUID]conversations.Conversation
}

func (f fakeConversationRepo) Get(_ context.Context, id uuid.UUID) (conversations.Conversation, error) {
	c, ok := f.rows[id]
	if !ok {
		return conversations.Conversation{}, conversations.ErrNotFound
	}
	return c, nil
}

type fakeMessageRepo struct {
	messages.Repository
	mu        sync.Mutex
	rows      []messages.Message
	seq       map[uuid.UUID]int64
	postings  []messages.Posting
	failWith  error
	listCalls []messages.ListFilter
}

func (f *fakeMessageRepo) Post(_ context.Context, in messages.Posting) (messages.Message, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return messages.Message{}, false, f.failWith
	}
	n := in.Message
	for _, m := range f.rows {
		if m.ConversationID == n.ConversationID && m.Sender.ID == n.Sender.ID && m.IdempotencyKey == n.IdempotencyKey {
			return m, false, nil
		}
	}
	f.seq[n.ConversationID]++
	m := messages.Message{
		ID: uuid.New(), ConversationID: n.ConversationID, SequenceNumber: f.seq[n.ConversationID],
		Sender: n.Sender, IdempotencyKey: n.IdempotencyKey, ParentID: n.ParentID, Type: n.Type,
		Content: n.Content, Metadata: n.Metadata, CreatedAt: time.Unix(1_800_000_000, 0),
	}
	f.rows = append(f.rows, m)
	f.postings = append(f.postings, in)
	return m, true, nil
}

func (f *fakeMessageRepo) List(_ context.Context, q messages.ListFilter) ([]messages.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, q)
	var out []messages.Message
	for _, m := range f.rows {
		if m.ConversationID == q.ConversationID && m.SequenceNumber > q.AfterSeq && len(out) < q.Limit {
			out = append(out, m)
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

type fakeDirectory struct {
	fakeMemberships
	projects     map[uuid.UUID]uuid.UUID
	projectRoles map[[2]uuid.UUID]authz.ProjectRole
}

func (d fakeDirectory) ProjectMembership(ctx context.Context, project, user uuid.UUID) (authz.ProjectMembership, error) {
	ws, ok := d.projects[project]
	if !ok {
		return authz.ProjectMembership{}, authz.ErrNotMember
	}
	role, err := d.Role(ctx, ws, user)
	if err != nil {
		return authz.ProjectMembership{}, err
	}
	return authz.ProjectMembership{WorkspaceID: ws, WorkspaceRole: role, ProjectRole: d.projectRoles[[2]uuid.UUID{project, user}]}, nil
}

type fixture struct {
	svc         *messages.Service
	repo        *fakeMessageRepo
	contributor auth.Principal // developer, project contributor
	viewer      auth.Principal // developer, project viewer
	teammate    auth.Principal // developer, no project role
	stranger    auth.Principal // not in the workspace
	workspace   uuid.UUID
	project     uuid.UUID
	open        uuid.UUID // workspace-wide conversation
	scoped      uuid.UUID // conversation in project
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	newUser := func() auth.Principal { return auth.Principal{Kind: auth.KindUser, ID: uuid.New()} }
	f := fixture{
		contributor: newUser(), viewer: newUser(), teammate: newUser(), stranger: newUser(),
		workspace: uuid.New(), project: uuid.New(), open: uuid.New(), scoped: uuid.New(),
	}
	dir := fakeDirectory{
		fakeMemberships: fakeMemberships{
			{f.workspace, f.contributor.ID}: authz.RoleDeveloper,
			{f.workspace, f.viewer.ID}:      authz.RoleDeveloper,
			{f.workspace, f.teammate.ID}:    authz.RoleDeveloper,
		},
		projects: map[uuid.UUID]uuid.UUID{f.project: f.workspace},
		projectRoles: map[[2]uuid.UUID]authz.ProjectRole{
			{f.project, f.contributor.ID}: authz.ProjectRoleContributor,
			{f.project, f.viewer.ID}:      authz.ProjectRoleViewer,
		},
	}
	convs := fakeConversationRepo{rows: map[uuid.UUID]conversations.Conversation{
		f.open:   {ID: f.open, WorkspaceID: f.workspace},
		f.scoped: {ID: f.scoped, WorkspaceID: f.workspace, ProjectID: &f.project},
	}}
	f.repo = &fakeMessageRepo{seq: map[uuid.UUID]int64{}}
	f.svc = messages.NewService(f.repo, conversations.NewService(convs, authz.NewWithProjects(dir)))
	return f
}

func errKind(err error) errs.Kind {
	if e, ok := errs.As(err); ok {
		return e.Kind
	}
	return ""
}

func TestPostStoresTheMessageAsTheCallerWithItsEvent(t *testing.T) {
	f := newFixture(t)
	parent := uuid.New()
	m, created, err := f.svc.Post(t.Context(), f.contributor, f.scoped, messages.PostInput{
		IdempotencyKey: "req-1", ParentID: &parent, Type: messages.TypeTask, Content: "ship it",
		Metadata: json.RawMessage(`{"a":1}`),
	})
	if err != nil || !created {
		t.Fatalf("Post = created %v, err %v", created, err)
	}
	if m.Sender.Kind != conversations.KindUser || m.Sender.ID != f.contributor.ID || m.SequenceNumber != 1 ||
		m.IdempotencyKey != "req-1" || m.Type != messages.TypeTask || m.Content != "ship it" {
		t.Errorf("message = %+v", m)
	}
	if len(f.repo.postings) != 1 {
		t.Fatalf("postings = %d, want 1", len(f.repo.postings))
	}
	got := f.repo.postings[0]
	if got.WorkspaceID != f.workspace || got.ProjectID == nil || *got.ProjectID != f.project ||
		got.Message.ParentID == nil || *got.Message.ParentID != parent {
		t.Errorf("posting = %+v, want the conversation's workspace and project", got)
	}
}

func TestPostDefaultsToTextAndGeneratesAKeyWhenNoneIsGiven(t *testing.T) {
	f := newFixture(t)
	a, _, err := f.svc.Post(t.Context(), f.teammate, f.open, messages.PostInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	b, created, err := f.svc.Post(t.Context(), f.teammate, f.open, messages.PostInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != messages.TypeText || a.IdempotencyKey == "" {
		t.Errorf("first = %+v, want type text and a generated key", a)
	}
	if !created || a.IdempotencyKey == b.IdempotencyKey || b.SequenceNumber != 2 {
		t.Errorf("without keys two posts are two messages; got keys %q, %q, created %v", a.IdempotencyKey, b.IdempotencyKey, created)
	}
}

func TestPostWithARepeatedKeyReturnsTheOriginal(t *testing.T) {
	f := newFixture(t)
	first, _, err := f.svc.Post(t.Context(), f.teammate, f.open, messages.PostInput{IdempotencyKey: "k", Content: "one"})
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := f.svc.Post(t.Context(), f.teammate, f.open, messages.PostInput{IdempotencyKey: "k", Content: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != first.ID || again.Content != "one" {
		t.Errorf("retry = %+v (created %v), want the original", again, created)
	}
}

func TestPostAuthorization(t *testing.T) {
	f := newFixture(t)
	agent := auth.Principal{Kind: "agent", ID: uuid.New()}
	tests := []struct {
		name         string
		p            auth.Principal
		conversation uuid.UUID
		want         errs.Kind
	}{
		{"member posts to a workspace-wide conversation", f.teammate, f.open, ""},
		{"project contributor posts in the project", f.contributor, f.scoped, ""},
		{"project viewer cannot post", f.viewer, f.scoped, errs.KindForbidden},
		{"member without a project role cannot see it", f.teammate, f.scoped, errs.KindNotFound},
		{"non-member cannot see it", f.stranger, f.open, errs.KindNotFound},
		{"unknown conversation", f.teammate, uuid.New(), errs.KindNotFound},
		{"non-user principal", agent, f.open, errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := f.svc.Post(t.Context(), tt.p, tt.conversation, messages.PostInput{Content: "hi"})
			if errKind(err) != tt.want || (tt.want == "" && err != nil) {
				t.Errorf("err = %v, want kind %q", err, tt.want)
			}
		})
	}
	if len(f.repo.postings) != 2 {
		t.Errorf("stored %d messages, want only the 2 allowed", len(f.repo.postings))
	}
}

func TestPostValidation(t *testing.T) {
	f := newFixture(t)
	nilParent := uuid.Nil
	tests := []struct {
		name  string
		in    messages.PostInput
		field string
	}{
		{"empty content", messages.PostInput{Content: "  "}, "content"},
		{"content too long", messages.PostInput{Content: strings.Repeat("x", messages.MaxContentLen+1)}, "content"},
		{"system messages are the platform's", messages.PostInput{Content: "hi", Type: messages.TypeSystem}, "message_type"},
		{"unknown type", messages.PostInput{Content: "hi", Type: "poll"}, "message_type"},
		{"metadata must be an object", messages.PostInput{Content: "hi", Metadata: json.RawMessage(`[1]`)}, "metadata"},
		{"key too long", messages.PostInput{Content: "hi", IdempotencyKey: strings.Repeat("k", 256)}, "idempotency_key"},
		{"nil parent", messages.PostInput{Content: "hi", ParentID: &nilParent}, "parent_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := f.svc.Post(t.Context(), f.teammate, f.open, tt.in)
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.KindInvalid || e.Fields[tt.field] == "" {
				t.Errorf("err = %v, want invalid %s", err, tt.field)
			}
		})
	}
	if len(f.repo.postings) != 0 {
		t.Errorf("stored %d invalid messages", len(f.repo.postings))
	}
}

func TestPostMapsRepositoryErrors(t *testing.T) {
	f := newFixture(t)
	f.repo.failWith = messages.ErrParentNotFound
	_, _, err := f.svc.Post(t.Context(), f.teammate, f.open, messages.PostInput{Content: "hi"})
	if e, ok := errs.As(err); !ok || e.Kind != errs.KindInvalid || e.Fields["parent_id"] == "" {
		t.Errorf("missing parent: err = %v, want invalid parent_id", err)
	}
	f.repo.failWith = messages.ErrConversationNotFound
	if _, _, err := f.svc.Post(t.Context(), f.teammate, f.open, messages.PostInput{Content: "hi"}); errKind(err) != errs.KindNotFound {
		t.Errorf("deleted conversation: err = %v, want not found", err)
	}
}

func TestListPagesBySequenceNumber(t *testing.T) {
	f := newFixture(t)
	for range 3 {
		if _, _, err := f.svc.Post(t.Context(), f.contributor, f.scoped, messages.PostInput{Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.svc.List(t.Context(), f.viewer, f.scoped, messages.ListInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	seqs := func(ms []messages.Message) []int64 {
		var out []int64
		for _, m := range ms {
			out = append(out, m.SequenceNumber)
		}
		return out
	}
	if !slices.Equal(seqs(page.Items), []int64{1, 2}) || page.Next == nil || *page.Next != 2 {
		t.Fatalf("first page = %v next %v, want [1 2] next 2", seqs(page.Items), page.Next)
	}
	page, err = f.svc.List(t.Context(), f.viewer, f.scoped, messages.ListInput{AfterSeq: *page.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seqs(page.Items), []int64{3}) || page.Next != nil {
		t.Errorf("last page = %v next %v, want [3] and no next", seqs(page.Items), page.Next)
	}
	if last := f.repo.listCalls[len(f.repo.listCalls)-1]; last.Limit != 3 || last.ConversationID != f.scoped {
		t.Errorf("repository asked for %+v, want limit+1 rows of the conversation", last)
	}
}

func TestListNeedsToSeeTheConversation(t *testing.T) {
	f := newFixture(t)
	for _, p := range []auth.Principal{f.teammate, f.stranger} {
		if _, err := f.svc.List(t.Context(), p, f.scoped, messages.ListInput{Limit: 10}); errKind(err) != errs.KindNotFound {
			t.Errorf("List by %v: err = %v, want not found", p.ID, err)
		}
	}
	if _, err := f.svc.List(t.Context(), f.teammate, f.open, messages.ListInput{AfterSeq: -1, Limit: 10}); errKind(err) != errs.KindInvalid {
		t.Errorf("negative after_seq: err = %v, want invalid", err)
	}
}
