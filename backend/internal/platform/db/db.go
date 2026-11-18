// Package db holds the Postgres connection pool and transaction helper.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open parses url and returns a pool. It does not dial, so the API can
// start and report "not ready" while Postgres is still coming up.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Check returns a readiness check that pings the pool.
func Check(pool *pgxpool.Pool) func(ctx context.Context) error {
	return pool.Ping
}

// Beginner starts transactions. *pgxpool.Pool and pgx.Tx satisfy it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn in a transaction. It commits if fn returns nil and rolls
// back otherwise, returning fn's error unchanged.
func WithTx(ctx context.Context, b Beginner, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			rollback(ctx, tx)
			panic(p)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		rollback(ctx, tx)
		return err
	}
	return tx.Commit(ctx)
}

// Rollback must run even if ctx was cancelled, or the connection stays
// checked out and pool.Close blocks forever.
func rollback(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(context.WithoutCancel(ctx))
}
