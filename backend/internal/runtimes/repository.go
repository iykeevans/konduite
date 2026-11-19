package runtimes

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) Create(ctx context.Context, n NewRuntime) (Runtime, uuid.UUID, error) {
	var (
		rt     store.AgentRuntimeInstance
		credID uuid.UUID
	)
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		rt, err = q.InsertRuntime(ctx, store.InsertRuntimeParams{WorkspaceID: n.WorkspaceID, OwnerID: n.OwnerID, Name: n.Name})
		if err != nil {
			return err
		}
		cred, err := q.InsertRuntimeCredential(ctx, store.InsertRuntimeCredentialParams{RuntimeID: rt.ID, SecretHash: n.SecretHash})
		credID = cred.ID
		return err
	})
	if err != nil {
		return Runtime{}, uuid.Nil, err
	}
	return Runtime{ID: rt.ID, WorkspaceID: rt.WorkspaceID, OwnerID: rt.OwnerID, Name: rt.Name, CreatedAt: rt.CreatedAt}, credID, nil
}

func (r postgresRepository) Credential(ctx context.Context, id uuid.UUID) (Credential, error) {
	row, err := store.New(r.pool).GetRuntimeCredential(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrCredentialNotFound
	}
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		ID: row.ID, RuntimeID: row.RuntimeID, WorkspaceID: row.WorkspaceID, OwnerID: row.OwnerID, SecretHash: row.SecretHash,
	}, nil
}

func (r postgresRepository) TouchCredential(ctx context.Context, id uuid.UUID) error {
	return store.New(r.pool).TouchRuntimeCredential(ctx, id)
}

func (r postgresRepository) DeleteCredential(ctx context.Context, id uuid.UUID) error {
	return store.New(r.pool).DeleteRuntimeCredential(ctx, id)
}
