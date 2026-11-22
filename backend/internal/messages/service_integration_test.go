//go:build integration

package messages_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/messages"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/store"
)

type world struct {
	pool      *pgxpool.Pool
	svc       *messages.Service
	convs     *conversations.Service
	workspace uuid.UUID
	project   uuid.UUID
}

func newWorld(t *testing.T) world {
	t.Helper()
	pool := dbtest.New(t)
	w := world{pool: pool}
	if err := pool.QueryRow(t.Context(), `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', $1, $2) RETURNING id`,
		"acme-"+uuid.NewString()[:8], uuid.New()).Scan(&w.workspace); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO projects (workspace_id, name, created_by) VALUES ($1, 'api', $2) RETURNING id`,
		w.workspace, uuid.New()).Scan(&w.project); err != nil {
		t.Fatal(err)
	}
	az := authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))
	w.convs = conversations.NewService(conversations.NewPostgresRepository(pool), az)
	w.svc = messages.NewService(messages.NewPostgresRepository(pool), w.convs)
	return w
}

func (w world) member(t *testing.T, role authz.Role, projectRole authz.ProjectRole) auth.Principal {
	t.Helper()
	p := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	if _, err := w.pool.Exec(t.Context(), `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
		w.workspace, p.ID, role); err != nil {
		t.Fatal(err)
	}
	if projectRole != "" {
		if _, err := w.pool.Exec(t.Context(), `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`,
			w.project, p.ID, projectRole); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (w world) conversation(t *testing.T, creator auth.Principal, project *uuid.UUID) uuid.UUID {
	t.Helper()
	c, err := w.convs.Create(t.Context(), creator, conversations.CreateInput{WorkspaceID: w.workspace, ProjectID: project})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

type outboxRow struct {
	EventType   string
	AggregateID uuid.UUID
	Payload     struct {
		WorkspaceID string  `json:"workspace_id"`
		ProjectID   *string `json:"project_id"`
		Data        struct {
			MessageID      string `json:"message_id"`
			ConversationID string `json:"conversation_id"`
			SequenceNumber int64  `json:"sequence_number"`
		} `json:"data"`
	}
}

func (w world) events(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := w.pool.Query(t.Context(), `SELECT event_type, aggregate_id, payload FROM outbox_events WHERE event_type = 'message.created' ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var (
			r   outboxRow
			raw []byte
		)
		if err := rows.Scan(&r.EventType, &r.AggregateID, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (w world) isParticipant(t *testing.T, conversation uuid.UUID, user uuid.UUID) bool {
	t.Helper()
	var ok bool
	if err := w.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM conversation_participants
		WHERE conversation_id = $1 AND participant_type = 'user' AND participant_id = $2)`, conversation, user).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestPostCommitsTheMessageItsEventAndTheSenderAsParticipant(t *testing.T) {
	w := newWorld(t)
	owner := w.member(t, authz.RoleOwner, "")
	contributor := w.member(t, authz.RoleDeveloper, authz.ProjectRoleContributor)
	c := w.conversation(t, owner, &w.project)
	if w.isParticipant(t, c, contributor.ID) {
		t.Fatal("contributor is already a participant")
	}

	m, created, err := w.svc.Post(t.Context(), contributor, c, messages.PostInput{IdempotencyKey: "req-1", Content: "hello"})
	if err != nil || !created {
		t.Fatalf("Post = created %v, err %v", created, err)
	}
	stored, err := messages.NewPostgresRepository(w.pool).Get(t.Context(), m.ID)
	if err != nil || stored.SequenceNumber != 1 || stored.Sender.ID != contributor.ID || stored.Content != "hello" {
		t.Fatalf("stored = %+v, err %v", stored, err)
	}
	if lastSeq(t, w.pool, c) != 1 {
		t.Errorf("last_seq = %d, want 1", lastSeq(t, w.pool, c))
	}
	evs := w.events(t)
	if len(evs) != 1 {
		t.Fatalf("message.created rows = %d, want 1", len(evs))
	}
	ev := evs[0]
	if ev.AggregateID != m.ID || ev.Payload.Data.MessageID != m.ID.String() || ev.Payload.Data.ConversationID != c.String() ||
		ev.Payload.Data.SequenceNumber != 1 || ev.Payload.WorkspaceID != w.workspace.String() ||
		ev.Payload.ProjectID == nil || *ev.Payload.ProjectID != w.project.String() {
		t.Errorf("event = %+v", ev)
	}
	if !w.isParticipant(t, c, contributor.ID) {
		t.Error("sender was not added as a participant")
	}
}

func TestRetriedPostReturnsTheOriginalWithoutASecondEvent(t *testing.T) {
	w := newWorld(t)
	alice := w.member(t, authz.RoleDeveloper, "")
	c := w.conversation(t, alice, nil)

	first, _, err := w.svc.Post(t.Context(), alice, c, messages.PostInput{IdempotencyKey: "k", Content: "one"})
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := w.svc.Post(t.Context(), alice, c, messages.PostInput{IdempotencyKey: "k", Content: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != first.ID || again.Content != "one" {
		t.Errorf("retry = %+v (created %v), want the original", again, created)
	}
	if n := len(w.events(t)); n != 1 || lastSeq(t, w.pool, c) != 1 {
		t.Errorf("after a retry: %d events, last_seq %d; want 1 and 1", n, lastSeq(t, w.pool, c))
	}
}

func TestPostIsAuthorizedAndWritesNothingWhenRefused(t *testing.T) {
	w := newWorld(t)
	owner := w.member(t, authz.RoleOwner, "")
	viewer := w.member(t, authz.RoleDeveloper, authz.ProjectRoleViewer)
	outsider := w.member(t, authz.RoleDeveloper, "")
	stranger := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	scoped := w.conversation(t, owner, &w.project)
	open := w.conversation(t, owner, nil)

	tests := []struct {
		name         string
		p            auth.Principal
		conversation uuid.UUID
		want         errs.Kind
	}{
		{"project viewer", viewer, scoped, errs.KindForbidden},
		{"member without a project role", outsider, scoped, errs.KindNotFound},
		{"not in the workspace", stranger, open, errs.KindNotFound},
		{"unknown conversation", owner, uuid.New(), errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := w.svc.Post(t.Context(), tt.p, tt.conversation, messages.PostInput{Content: "hi"})
			if errKind(err) != tt.want {
				t.Errorf("err = %v, want %s", err, tt.want)
			}
		})
	}

	missing := uuid.New()
	_, _, err := w.svc.Post(t.Context(), owner, open, messages.PostInput{Content: "reply", ParentID: &missing})
	if e, ok := errs.As(err); !ok || e.Fields["parent_id"] == "" {
		t.Errorf("unknown parent: err = %v, want invalid parent_id", err)
	}
	if n := len(w.events(t)); n != 0 || lastSeq(t, w.pool, scoped) != 0 || lastSeq(t, w.pool, open) != 0 {
		t.Errorf("refused posts wrote %d events, last_seq %d/%d", n, lastSeq(t, w.pool, scoped), lastSeq(t, w.pool, open))
	}
	if w.isParticipant(t, scoped, viewer.ID) {
		t.Error("a refused post joined the viewer to the conversation")
	}
}

func TestConcurrentPostsGetDistinctGaplessSequenceNumbers(t *testing.T) {
	w := newWorld(t)
	c := w.conversation(t, w.member(t, authz.RoleOwner, ""), nil)
	const senders, perSender = 5, 4
	people := make([]auth.Principal, senders)
	for i := range people {
		people[i] = w.member(t, authz.RoleDeveloper, "")
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seqs []int64
		ids  = map[uuid.UUID]bool{}
	)
	errc := make(chan error, senders*perSender*2)
	for _, p := range people {
		for i := range perSender {
			for range 2 {
				wg.Go(func() {
					m, _, err := w.svc.Post(t.Context(), p, c, messages.PostInput{IdempotencyKey: fmt.Sprint("k", i), Content: "hi"})
					if err != nil {
						errc <- err
						return
					}
					mu.Lock()
					defer mu.Unlock()
					if !ids[m.ID] {
						ids[m.ID] = true
						seqs = append(seqs, m.SequenceNumber)
					}
				})
			}
		}
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}

	slices.Sort(seqs)
	want := make([]int64, senders*perSender)
	for i := range want {
		want[i] = int64(i + 1)
	}
	if !slices.Equal(seqs, want) {
		t.Errorf("sequence numbers = %v, want 1..%d", seqs, len(want))
	}
	if n := len(w.events(t)); n != len(want) || lastSeq(t, w.pool, c) != int64(len(want)) {
		t.Errorf("%d events, last_seq %d; want %d of each", n, lastSeq(t, w.pool, c), len(want))
	}
}

func TestListReturnsMessagesInSequenceOrder(t *testing.T) {
	w := newWorld(t)
	alice := w.member(t, authz.RoleDeveloper, "")
	bob := w.member(t, authz.RoleDeveloper, "")
	c := w.conversation(t, alice, nil)
	for i, p := range []auth.Principal{alice, bob, alice} {
		if _, _, err := w.svc.Post(t.Context(), p, c, messages.PostInput{Content: fmt.Sprint("m", i)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := w.svc.List(t.Context(), bob, c, messages.ListInput{AfterSeq: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].SequenceNumber != 2 || page.Items[0].Content != "m1" || page.Next == nil || *page.Next != 2 {
		t.Errorf("page = %+v", page)
	}
	page, err = w.svc.List(t.Context(), bob, c, messages.ListInput{AfterSeq: *page.Next, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].SequenceNumber != 3 || page.Next != nil {
		t.Errorf("last page = %+v, err %v", page, err)
	}
}
