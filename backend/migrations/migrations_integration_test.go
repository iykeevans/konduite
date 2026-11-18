//go:build integration

package migrations_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/migrations"
)

var tables = []string{
	"workspaces", "workspace_members", "invitations", "projects", "project_members",
	"audit_logs", "outbox_events", "inbound_events",
}

func appTables(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	return got
}

func TestMigrationsGoUpDownAndUpAgain(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	ctx := t.Context()

	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("up: %v", err)
	}
	got := appTables(t, pool)
	for _, name := range tables {
		if !got[name] {
			t.Errorf("table %s missing after up", name)
		}
	}

	if err := migrations.Down(ctx, pool); err != nil {
		t.Fatalf("down: %v", err)
	}
	if left := appTables(t, pool); len(left) != 0 {
		t.Errorf("tables left after down to 0: %v", left)
	}

	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("second up: %v", err)
	}
}
