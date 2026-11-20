package agents

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

const (
	actionCreated      = "agent.created"
	actionUpdated      = "agent.updated"
	actionDeleted      = "agent.deleted"
	identityConstraint = "agents_runtime_id_provider_name_key"
)

// NewPostgresRegistryRepository returns a RegistryRepository backed by
// Postgres.
func NewPostgresRegistryRepository(pool *pgxpool.Pool) RegistryRepository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) Runtime(ctx context.Context, id uuid.UUID) (RuntimeOwner, error) {
	row, err := store.New(r.pool).GetAgentRuntime(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeOwner{}, ErrRuntimeNotFound
	}
	if err != nil {
		return RuntimeOwner{}, err
	}
	return RuntimeOwner{ID: row.ID, WorkspaceID: row.WorkspaceID, OwnerID: row.OwnerID}, nil
}

func (r postgresRepository) Create(ctx context.Context, n NewAgent) (Agent, error) {
	var agent Agent
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		// Same lock as Sync, so a registration and a hello from the runtime
		// can't race on the (runtime, provider, name) row.
		workspaceID, err := q.LockRuntimeForAgentSync(ctx, n.Runtime.ID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && workspaceID != n.Runtime.WorkspaceID) {
			return ErrRuntimeNotFound
		}
		if err != nil {
			return err
		}
		caps, err := json.Marshal(n.Capabilities)
		if err != nil {
			return err
		}
		row, err := q.InsertRegisteredAgent(ctx, store.InsertRegisteredAgentParams{
			WorkspaceID: n.Runtime.WorkspaceID, RuntimeID: n.Runtime.ID, Provider: n.Provider, Name: n.Name,
			DisplayName: nullable(n.DisplayName), Description: nullable(n.Description), Capabilities: caps,
		})
		if err != nil {
			return err
		}
		if agent, err = fromRow(row); err != nil {
			return err
		}
		return audit(ctx, q, agent, n.Actor, actionCreated, map[string]string{"provider": agent.Provider, "name": agent.Name})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == identityConstraint {
		return Agent{}, ErrAgentExists
	}
	return agent, err
}

func (r postgresRepository) Get(ctx context.Context, id uuid.UUID) (Agent, error) {
	row, err := store.New(r.pool).GetAgent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	return fromRow(row)
}

func (r postgresRepository) List(ctx context.Context, f ListFilter) ([]Agent, error) {
	params := store.ListAgentsParams{
		WorkspaceID: f.WorkspaceID,
		RowLimit:    int32(min(f.Limit, 1001)), //nolint:gosec // bounded just above
	}
	if f.RuntimeID != uuid.Nil {
		params.RuntimeID = &f.RuntimeID
	}
	if f.After != nil {
		params.AfterCreatedAt, params.AfterID = &f.After.CreatedAt, &f.After.ID
	}
	rows, err := store.New(r.pool).ListAgents(ctx, params)
	if err != nil {
		return nil, err
	}
	return fromRows(rows)
}

func (r postgresRepository) RuntimeAgents(ctx context.Context, runtimeID uuid.UUID) ([]Agent, error) {
	rows, err := store.New(r.pool).ListRuntimeAgents(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	return fromRows(rows)
}

type change struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (r postgresRepository) Update(ctx context.Context, id uuid.UUID, actor Actor, in UpdateInput) (Agent, error) {
	var agent Agent
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.LockAgent(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if agent, err = fromRow(row); err != nil {
			return err
		}
		changes := map[string]change{}
		apply := func(field string, to, dst *string) {
			if to != nil && *to != *dst {
				changes[field] = change{From: *dst, To: *to}
				*dst = *to
			}
		}
		next := agent
		apply("display_name", in.DisplayName, &next.DisplayName)
		apply("description", in.Description, &next.Description)
		if len(changes) == 0 {
			return nil
		}
		row, err = q.UpdateAgentLabels(ctx, store.UpdateAgentLabelsParams{
			ID: id, DisplayName: nullable(next.DisplayName), Description: nullable(next.Description),
		})
		if err != nil {
			return err
		}
		if agent, err = fromRow(row); err != nil {
			return err
		}
		return audit(ctx, q, agent, actor, actionUpdated, map[string]any{"changes": changes})
	})
	return agent, err
}

func (r postgresRepository) Delete(ctx context.Context, id uuid.UUID, actor Actor) error {
	return db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.LockAgent(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		agent, err := fromRow(row)
		if err != nil {
			return err
		}
		if _, err := q.DeleteAgent(ctx, id); err != nil {
			return err
		}
		return audit(ctx, q, agent, actor, actionDeleted, map[string]string{"provider": agent.Provider, "name": agent.Name})
	})
}

func audit(ctx context.Context, q *store.Queries, a Agent, actor Actor, action string, metadata any) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		WorkspaceID:  &a.WorkspaceID,
		ActorType:    actor.Type,
		ActorID:      &actor.ID,
		Action:       action,
		ResourceType: "agent",
		ResourceID:   &a.ID,
		Metadata:     meta,
	})
}

func fromRows(rows []store.Agent) ([]Agent, error) {
	out := make([]Agent, len(rows))
	for i, row := range rows {
		a, err := fromRow(row)
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
