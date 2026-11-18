//go:build integration

package dbtest_test

import (
	"testing"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
)

func TestEachCallGetsAnIsolatedDatabase(t *testing.T) {
	a, b := dbtest.New(t), dbtest.New(t)

	if _, err := a.Exec(t.Context(), `CREATE TABLE marker (id int)`); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := b.QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'marker')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("table created through one pool is visible through the other")
	}
}
