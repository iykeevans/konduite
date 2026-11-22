package agents

import (
	"context"
	"encoding/json"
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

func (r postgresRepository) Sync(ctx context.Context, rt Runtime, announced []Announcement) ([]Agent, error) {
	out := make([]Agent, 0, len(announced))
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		// The row lock serialises hellos from one runtime, so a reconnect
		// racing an older connection can't interleave upserts and mark-offline.
		workspaceID, err := q.LockRuntimeForAgentSync(ctx, rt.ID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && workspaceID != rt.WorkspaceID) {
			return ErrRuntimeNotFound
		}
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(announced))
		for _, a := range announced {
			caps, err := json.Marshal(a.Capabilities)
			if err != nil {
				return err
			}
			var version *string
			if a.Version != "" {
				version = &a.Version
			}
			row, err := q.UpsertAnnouncedAgent(ctx, store.UpsertAnnouncedAgentParams{
				WorkspaceID: rt.WorkspaceID, RuntimeID: rt.ID, Provider: a.Provider, Name: a.Name,
				Version: version, Capabilities: caps,
			})
			if err != nil {
				return err
			}
			agent, err := fromRow(row)
			if err != nil {
				return err
			}
			out = append(out, agent)
			ids = append(ids, row.ID)
		}
		return q.MarkUnannouncedAgentsOffline(ctx, store.MarkUnannouncedAgentsOfflineParams{RuntimeID: rt.ID, AnnouncedIds: ids})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r postgresRepository) HasProjectAccess(ctx context.Context, agentID, projectID uuid.UUID) (bool, error) {
	return store.New(r.pool).AgentHasProjectAccess(ctx, store.AgentHasProjectAccessParams{AgentID: agentID, ProjectID: projectID})
}

func fromRow(row store.Agent) (Agent, error) {
	a := Agent{
		ID: row.ID, WorkspaceID: row.WorkspaceID, RuntimeID: row.RuntimeID, Provider: row.Provider, Name: row.Name,
		Status: Status(row.Status), LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.Version != nil {
		a.Version = *row.Version
	}
	if row.DisplayName != nil {
		a.DisplayName = *row.DisplayName
	}
	if row.Description != nil {
		a.Description = *row.Description
	}
	if err := json.Unmarshal(row.Capabilities, &a.Capabilities); err != nil {
		return Agent{}, err
	}
	return a, nil
}
