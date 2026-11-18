//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := dbtest.New(t)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE notes (body text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func countNotes(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWithTxCommitsWhenFnSucceeds(t *testing.T) {
	pool := setup(t)

	err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO notes (body) VALUES ('kept')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := countNotes(t, pool); n != 1 {
		t.Errorf("notes = %d after commit, want 1", n)
	}
}

var errBoom = errors.New("boom")

func TestWithTxRollsBackWhenFnFails(t *testing.T) {
	pool := setup(t)

	err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notes (body) VALUES ('discarded')`); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}
	if n := countNotes(t, pool); n != 0 {
		t.Errorf("notes = %d after failed fn, want 0", n)
	}
}

func TestWithTxRollsBackAndRepanicsWhenFnPanics(t *testing.T) {
	pool := setup(t)

	func() {
		defer func() {
			if r := recover(); r != "kaboom" {
				t.Errorf("recovered %v, want the original panic value kaboom", r)
			}
		}()
		_ = db.WithTx(t.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO notes (body) VALUES ('discarded')`); err != nil {
				return err
			}
			panic("kaboom")
		})
	}()

	if n := countNotes(t, pool); n != 0 {
		t.Errorf("notes = %d after panic, want 0", n)
	}
}

func TestCheckPassesWhenDatabaseIsUp(t *testing.T) {
	if err := db.Check(dbtest.New(t))(t.Context()); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
}
