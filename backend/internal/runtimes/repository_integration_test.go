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

	if err := repo.DeleteCredential(ctx, credID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Credential(ctx, credID); !errors.Is(err, runtimes.ErrCredentialNotFound) {
		t.Errorf("deleted credential: err = %v, want ErrCredentialNotFound", err)
	}
	if err := repo.DeleteCredential(ctx, credID); err != nil {
		t.Errorf("deleting an already deleted credential: %v, want nil", err)
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

func TestPostgresRepositoryGetsAndListsRuntimesWithCredentialState(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	owner := uuid.New()
	var workspace, other uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Acme', 'acme', $1) RETURNING id`,
		owner).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug, created_by) VALUES ('Other', 'other', $1) RETURNING id`,
		owner).Scan(&other); err != nil {
		t.Fatal(err)
	}
	repo := runtimes.NewPostgresRepository(pool)
	hash := bytes.Repeat([]byte{7}, 32)
	var ids []uuid.UUID
	var creds []uuid.UUID
	for _, name := range []string{"a", "b", "c"} {
		rt, credID, err := repo.Create(ctx, runtimes.NewRuntime{WorkspaceID: workspace, OwnerID: owner, Name: name, SecretHash: hash})
		if err != nil {
			t.Fatal(err)
		}
		ids, creds = append(ids, rt.ID), append(creds, credID)
	}
	if _, _, err := repo.Create(ctx, runtimes.NewRuntime{WorkspaceID: other, OwnerID: owner, Name: "x", SecretHash: hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime_instances SET created_at = now() - make_interval(secs => $2) WHERE id = $1`,
		ids[0], 30); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ids[1] || got.WorkspaceID != workspace || got.OwnerID != owner || got.Name != "b" ||
		got.Revoked || got.CredentialLastUsedAt != nil || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("runtime = %+v, want b with an unused, live credential", got)
	}
	if err := repo.TouchCredential(ctx, creds[1]); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, ids[1]); got.CredentialLastUsedAt == nil {
		t.Error("credential_last_used_at still null after a token exchange")
	}
	if err := repo.DeleteCredential(ctx, creds[1]); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, ids[1]); !got.Revoked {
		t.Error("runtime not revoked after its credential was deleted")
	}
	if _, err := repo.Get(ctx, uuid.New()); !errors.Is(err, runtimes.ErrNotFound) {
		t.Errorf("unknown runtime: err = %v, want ErrNotFound", err)
	}

	first, err := repo.List(ctx, runtimes.ListFilter{WorkspaceID: workspace, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[1].ID == ids[0] {
		t.Fatalf("first page = %+v, want the two newest runtimes", first)
	}
	last := first[len(first)-1]
	rest, err := repo.List(ctx, runtimes.ListFilter{
		WorkspaceID: workspace, Limit: 2, After: &runtimes.Position{CreatedAt: last.CreatedAt, ID: last.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].ID != ids[0] {
		t.Errorf("second page = %+v, want only the oldest runtime", rest)
	}
}
