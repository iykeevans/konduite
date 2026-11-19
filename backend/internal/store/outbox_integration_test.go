//go:build integration

package store_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/store"
)

func TestOutboxEventRoundTrip(t *testing.T) {
	q := store.New(dbtest.New(t))
	id, aggregate, workspace := uuid.New(), uuid.New(), uuid.New()

	created, err := q.InsertOutboxEvent(t.Context(), store.InsertOutboxEventParams{
		ID:          id,
		EventType:  "workspace.created",
		AggregateID: aggregate,
		WorkspaceID: &workspace,
		Payload:     json.RawMessage(`{"name":"Acme"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	pending, err := q.ListUnpublishedOutboxEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("unpublished = %d events, want 1", len(pending))
	}
	got := pending[0]
	if created.ID != id || got.ID != id || got.EventType != "workspace.created" || got.AggregateID != aggregate ||
		got.WorkspaceID == nil || *got.WorkspaceID != workspace || got.PublishedAt != nil {
		t.Errorf("event = %+v, want the inserted event, unpublished", got)
	}
	if string(got.Payload) != `{"name": "Acme"}` {
		t.Errorf("payload = %s, want the stored JSON", got.Payload)
	}
}
