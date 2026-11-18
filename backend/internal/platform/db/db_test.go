package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iykeevans/konduite/backend/internal/platform/db"
)

func TestOpenRejectsMalformedURL(t *testing.T) {
	_, err := db.Open(t.Context(), "postgres://user:s3cret-pw@host:notaport/db")
	if err == nil {
		t.Fatal("want an error for a malformed DATABASE_URL")
	}
	if strings.Contains(err.Error(), "s3cret-pw") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestCheckFailsWhenDatabaseIsUnreachable(t *testing.T) {
	pool, err := db.Open(t.Context(), "postgres://postgres:postgres@127.0.0.1:1/postgres?connect_timeout=1")
	if err != nil {
		t.Fatalf("Open should not dial, so an unreachable host is not an error yet: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := db.Check(pool)(ctx); err == nil {
		t.Fatal("Check returned nil for an unreachable database")
	}
}
