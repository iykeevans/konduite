package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
	"github.com/iykeevans/konduite/backend/internal/store"
)

type fakeOutbox struct {
	rows []store.InsertOutboxEventParams
	err  error
}

func (f *fakeOutbox) InsertOutboxEvent(_ context.Context, arg store.InsertOutboxEventParams) (store.OutboxEvent, error) {
	if f.err != nil {
		return store.OutboxEvent{}, f.err
	}
	f.rows = append(f.rows, arg)
	return store.OutboxEvent{ID: arg.ID, EventType: arg.EventType, AggregateID: arg.AggregateID, WorkspaceID: arg.WorkspaceID, Payload: arg.Payload}, nil
}

type envelope struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Version       int             `json:"version"`
	OccurredAt    time.Time       `json:"occurred_at"`
	WorkspaceID   string          `json:"workspace_id"`
	ProjectID     *string         `json:"project_id"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   *string         `json:"causation_id"`
	Data          json.RawMessage `json:"data"`
}

func connected(workspace, runtime uuid.UUID) events.Event {
	return events.Event{
		Type:        "runtime.connected",
		Version:     1,
		WorkspaceID: workspace,
		AggregateID: runtime,
		Data:        map[string]string{"runtime_id": runtime.String()},
	}
}

func onlyRow(t *testing.T, out *fakeOutbox) (store.InsertOutboxEventParams, envelope) {
	t.Helper()
	if len(out.rows) != 1 {
		t.Fatalf("inserted %d rows, want 1", len(out.rows))
	}
	var env envelope
	if err := json.Unmarshal(out.rows[0].Payload, &env); err != nil {
		t.Fatalf("payload is not an envelope: %v", err)
	}
	return out.rows[0], env
}

func TestEnqueueWritesTheEnvelopeAsTheRow(t *testing.T) {
	workspace, project, runtime := uuid.New(), uuid.New(), uuid.New()
	ev := connected(workspace, runtime)
	ev.ProjectID = &project
	out := &fakeOutbox{}
	before := time.Now()

	id, err := events.Enqueue(t.Context(), out, ev)
	if err != nil {
		t.Fatal(err)
	}

	row, env := onlyRow(t, out)
	if row.ID != id || env.ID != id.String() {
		t.Errorf("row id %s, envelope id %s, returned %s: want all equal", row.ID, env.ID, id)
	}
	if row.EventType != "runtime.connected" || env.Type != "runtime.connected" || env.Version != 1 {
		t.Errorf("row type %q, envelope type %q v%d", row.EventType, env.Type, env.Version)
	}
	if row.AggregateID != runtime {
		t.Errorf("aggregate = %s, want %s", row.AggregateID, runtime)
	}
	if row.WorkspaceID == nil || *row.WorkspaceID != workspace || env.WorkspaceID != workspace.String() {
		t.Errorf("workspace: row %v, envelope %s, want %s", row.WorkspaceID, env.WorkspaceID, workspace)
	}
	if env.ProjectID == nil || *env.ProjectID != project.String() {
		t.Errorf("project = %v, want %s", env.ProjectID, project)
	}
	if env.OccurredAt.Before(before.Add(-time.Second)) || env.OccurredAt.After(time.Now().Add(time.Second)) {
		t.Errorf("occurred_at = %s, want now", env.OccurredAt)
	}
	if string(env.Data) != `{"runtime_id":"`+runtime.String()+`"}` {
		t.Errorf("data = %s", env.Data)
	}
	if env.CausationID != nil {
		t.Errorf("causation_id = %s, want it omitted", *env.CausationID)
	}
}

func TestEnqueueGivesEachEventANewID(t *testing.T) {
	out := &fakeOutbox{}
	ev := connected(uuid.New(), uuid.New())
	a, errA := events.Enqueue(t.Context(), out, ev)
	b, errB := events.Enqueue(t.Context(), out, ev)
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a == b || a == uuid.Nil {
		t.Errorf("ids %s and %s, want two distinct non-nil ids", a, b)
	}
}

func TestEnqueueCorrelationID(t *testing.T) {
	requestCorr := uuid.New()
	tests := []struct {
		name string
		ctx  func(context.Context) context.Context
		want func(t *testing.T, env envelope, id uuid.UUID)
	}{
		{
			name: "uuid from the request context is kept",
			ctx:  func(ctx context.Context) context.Context { return log.WithCorrelationID(ctx, requestCorr.String()) },
			want: func(t *testing.T, env envelope, _ uuid.UUID) {
				if env.CorrelationID != requestCorr.String() {
					t.Errorf("correlation_id = %s, want %s", env.CorrelationID, requestCorr)
				}
			},
		},
		{
			name: "no correlation id starts a chain at this event",
			ctx:  func(ctx context.Context) context.Context { return ctx },
			want: func(t *testing.T, env envelope, id uuid.UUID) {
				if env.CorrelationID != id.String() {
					t.Errorf("correlation_id = %s, want the event id %s", env.CorrelationID, id)
				}
			},
		},
		{
			name: "non-uuid token maps to a stable uuid",
			ctx:  func(ctx context.Context) context.Context { return log.WithCorrelationID(ctx, "req-abc.123") },
			want: func(t *testing.T, env envelope, _ uuid.UUID) {
				want := uuid.NewSHA1(uuid.NameSpaceURL, []byte("konduite:correlation:req-abc.123"))
				if env.CorrelationID != want.String() {
					t.Errorf("correlation_id = %s, want %s", env.CorrelationID, want)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &fakeOutbox{}
			id, err := events.Enqueue(tc.ctx(t.Context()), out, connected(uuid.New(), uuid.New()))
			if err != nil {
				t.Fatal(err)
			}
			_, env := onlyRow(t, out)
			tc.want(t, env, id)
		})
	}
}

func TestEnqueueCarriesCausation(t *testing.T) {
	cause := uuid.New()
	ev := connected(uuid.New(), uuid.New())
	ev.CausationID = &cause
	out := &fakeOutbox{}
	if _, err := events.Enqueue(t.Context(), out, ev); err != nil {
		t.Fatal(err)
	}
	_, env := onlyRow(t, out)
	if env.CausationID == nil || *env.CausationID != cause.String() {
		t.Errorf("causation_id = %v, want %s", env.CausationID, cause)
	}
}

func TestEnqueueRejectsInvalidEventsWithoutInserting(t *testing.T) {
	runtime := uuid.New()
	tests := []struct {
		name   string
		mutate func(*events.Event)
	}{
		{"unknown type", func(e *events.Event) { e.Type = "runtime.exploded" }},
		{"unknown version", func(e *events.Event) { e.Version = 9 }},
		{"zero version", func(e *events.Event) { e.Version = 0 }},
		{"malformed type", func(e *events.Event) { e.Type = "Runtime Connected" }},
		{"missing workspace", func(e *events.Event) { e.WorkspaceID = uuid.Nil }},
		{"missing aggregate", func(e *events.Event) { e.AggregateID = uuid.Nil }},
		{"payload breaks the schema", func(e *events.Event) { e.Data = map[string]string{"runtime_id": "nope"} }},
		{"extra payload field", func(e *events.Event) {
			e.Data = map[string]string{"runtime_id": runtime.String(), "extra": "x"}
		}},
		{"payload is not an object", func(e *events.Event) { e.Data = []string{"x"} }},
		{"payload does not marshal", func(e *events.Event) { e.Data = make(chan int) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := connected(uuid.New(), runtime)
			tc.mutate(&ev)
			out := &fakeOutbox{}
			_, err := events.Enqueue(t.Context(), out, ev)
			if !errors.Is(err, events.ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
			if len(out.rows) != 0 {
				t.Errorf("inserted %d rows, want none", len(out.rows))
			}
		})
	}
}

func TestEnqueueRejectsCredentialsWithoutInserting(t *testing.T) {
	tests := []struct {
		name string
		data any
	}{
		{"token key", map[string]any{"runtime_id": uuid.NewString(), "token": "x"}},
		{"nested secret key", map[string]any{"runtime_id": uuid.NewString(), "meta": map[string]any{"Client_Secret": "x"}}},
		{"key inside an array", map[string]any{"items": []any{map[string]any{"api-key": "x"}}}},
		{"bearer value", map[string]any{"note": "Bearer abc.def"}},
		{"jwt value", map[string]any{"note": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"}},
		{"url with password", map[string]any{"dsn": "postgres://u:hunter2@db/x"}}, //nolint:gosec // test fixture, not a real credential
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := connected(uuid.New(), uuid.New())
			ev.Data = tc.data
			out := &fakeOutbox{}
			_, err := events.Enqueue(t.Context(), out, ev)
			if !errors.Is(err, events.ErrCredential) {
				t.Errorf("err = %v, want ErrCredential", err)
			}
			if len(out.rows) != 0 {
				t.Errorf("inserted %d rows, want none", len(out.rows))
			}
		})
	}
}

func TestEnqueueCredentialErrorDoesNotEchoTheValue(t *testing.T) {
	ev := connected(uuid.New(), uuid.New())
	ev.Data = map[string]any{"password": "hunter2"}
	_, err := events.Enqueue(t.Context(), &fakeOutbox{}, ev)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q leaks the secret value", err)
	}
}

func TestEnqueueReturnsInsertErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := events.Enqueue(t.Context(), &fakeOutbox{err: boom}, connected(uuid.New(), uuid.New()))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the insert error", err)
	}
}

func TestSubject(t *testing.T) {
	tests := map[string]string{
		"runtime.connected":     "agenthub.runtime.connected",
		"message.created":       "agenthub.message.created",
		"runtime.session.found": "agenthub.runtime.session.found",
	}
	for typ, want := range tests {
		if got := events.Subject(typ); got != want {
			t.Errorf("Subject(%q) = %q, want %q", typ, got, want)
		}
	}
}
