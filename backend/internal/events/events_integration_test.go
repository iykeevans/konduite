//go:build integration

package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/store"
)

func TestEnqueueCommitsWithTheTransaction(t *testing.T) {
	pool := dbtest.New(t)
	workspace, runtime := uuid.New(), uuid.New()

	var id uuid.UUID
	err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = events.Enqueue(ctx, store.New(tx), connected(workspace, runtime))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := store.New(pool).ListUnpublishedOutboxEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("unpublished = %d rows, want 1", len(rows))
	}
	row := rows[0]
	var env envelope
	if err := json.Unmarshal(row.Payload, &env); err != nil {
		t.Fatal(err)
	}
	if row.ID != id || env.ID != id.String() {
		t.Errorf("row id %s, envelope id %s, want both %s", row.ID, env.ID, id)
	}
	if row.EventType != env.Type || events.Subject(row.EventType) != "agenthub.runtime.connected" {
		t.Errorf("row type %q, envelope type %q", row.EventType, env.Type)
	}
	if row.AggregateID != runtime || row.WorkspaceID == nil || *row.WorkspaceID != workspace {
		t.Errorf("row = %+v, want aggregate %s in workspace %s", row, runtime, workspace)
	}
}

func TestEnqueueRollsBackWithTheTransaction(t *testing.T) {
	pool := dbtest.New(t)
	abort := errors.New("abort")

	err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := events.Enqueue(ctx, store.New(tx), connected(uuid.New(), uuid.New())); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("err = %v, want abort", err)
	}

	rows, err := store.New(pool).ListUnpublishedOutboxEvents(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("unpublished = %d rows after rollback, want 0", len(rows))
	}
}
