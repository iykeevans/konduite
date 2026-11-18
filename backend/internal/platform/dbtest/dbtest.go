// Package dbtest gives integration tests an isolated Postgres database.
//
// One container is started per test binary. Each New call clones a fresh
// database from a template, so tests can run in parallel without sharing rows.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	image    = "postgres:17-alpine" // matches supabase/config.toml db.major_version
	template = "konduite_template"
)

var (
	once     sync.Once
	adminURL string
	startErr error
)

// New returns a pool on a new, empty database that is dropped when t ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(start)
	if startErr != nil {
		t.Fatalf("dbtest: start postgres: %v", startErr)
	}
	ctx := context.Background()

	name := "test_" + randomHex()
	if err := adminExec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, name, template)); err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if err := adminExec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name)); err != nil {
			t.Errorf("dbtest: drop database %s: %v", name, err)
		}
	})
	return pool
}

// The container is never terminated explicitly: testcontainers' reaper
// removes it when the test binary exits.
func start() {
	ctx := context.Background()
	c, err := postgres.Run(ctx, image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		startErr = err
		return
	}
	if adminURL, err = c.ConnectionString(ctx, "sslmode=disable"); err != nil {
		startErr = err
		return
	}
	startErr = adminExec(ctx, `CREATE DATABASE `+template)
}

func adminExec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
