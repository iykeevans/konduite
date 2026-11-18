// Package migrations embeds the goose SQL migrations so the API, tests and
// the goose CLI (`make migrate`) all apply the same files.
package migrations

import (
	"context"
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// Up applies every pending migration.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	return withProvider(pool, func(p *goose.Provider) error {
		_, err := p.Up(ctx)
		return err
	})
}

// Down rolls back every applied migration.
func Down(ctx context.Context, pool *pgxpool.Pool) error {
	return withProvider(pool, func(p *goose.Provider) error {
		_, err := p.DownTo(ctx, 0)
		return err
	})
}

func withProvider(pool *pgxpool.Pool, fn func(*goose.Provider) error) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	sub, err := fs.Sub(files, ".")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return err
	}
	return fn(p)
}
