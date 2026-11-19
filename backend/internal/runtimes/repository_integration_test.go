//go:build integration

package runtimes_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
)

func TestPostgresRepositoryStoresARuntimeWithItsCredential(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	owner := uuid.New()
	var workspace uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', 'acme', $1) RETURNING id`,
		owner).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	repo := runtimes.NewPostgresRepository(pool)
	hash := bytes.Repeat([]byte{7}, 32)

	rt, credID, err := repo.Create(ctx, runtimes.NewRuntime{WorkspaceID: workspace, OwnerID: owner, Name: "laptop", SecretHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Credential(ctx, credID)
	if err != nil {
		t.Fatal(err)
	}
	want := runtimes.Credential{ID: credID, RuntimeID: rt.ID, WorkspaceID: workspace, OwnerID: owner, SecretHash: hash}
	if got.ID != want.ID || got.RuntimeID != want.RuntimeID || got.WorkspaceID != want.WorkspaceID ||
		got.OwnerID != want.OwnerID || !bytes.Equal(got.SecretHash, hash) {
		t.Errorf("credential = %+v, want %+v", got, want)
	}

	if err := repo.TouchCredential(ctx, credID); err != nil {
		t.Fatal(err)
	}
	var used bool
	if err := pool.QueryRow(ctx, `SELECT last_used_at IS NOT NULL FROM runtime_credentials WHERE id = $1`, credID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("last_used_at not set after TouchCredential")
	}

	if _, err := repo.Credential(ctx, uuid.New()); !errors.Is(err, runtimes.ErrCredentialNotFound) {
		t.Errorf("unknown credential: err = %v, want ErrCredentialNotFound", err)
	}
}

func TestPostgresRepositoryStoresNothingWhenTheCredentialFails(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	var workspace uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', 'acme', $1) RETURNING id`,
		uuid.New()).Scan(&workspace); err != nil {
		t.Fatal(err)
	}

	_, _, err := runtimes.NewPostgresRepository(pool).Create(ctx,
		runtimes.NewRuntime{WorkspaceID: workspace, OwnerID: uuid.New(), Name: "laptop", SecretHash: []byte("short")})
	if err == nil {
		t.Fatal("want an error for a malformed hash")
	}
	var runtimesLeft int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime_instances`).Scan(&runtimesLeft); err != nil {
		t.Fatal(err)
	}
	if runtimesLeft != 0 {
		t.Errorf("%d runtimes stored without a credential, want 0", runtimesLeft)
	}
}
