package workspaces

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

const (
	actionCreated  = "workspace.created"
	actionUpdated  = "workspace.updated"
	slugConstraint = "workspaces_slug_key"
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) Create(ctx context.Context, n NewWorkspace) (Workspace, error) {
	var ws Workspace
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.InsertWorkspace(ctx, store.InsertWorkspaceParams{Name: n.Name, Slug: n.Slug, CreatedBy: n.OwnerID})
		if err != nil {
			return err
		}
		if err := q.InsertWorkspaceMember(ctx, store.InsertWorkspaceMemberParams{
			WorkspaceID: row.ID, UserID: n.OwnerID, Role: string(authz.RoleOwner),
		}); err != nil {
			return err
		}
		ws = fromRow(row, authz.RoleOwner)
		return audit(ctx, q, row.ID, n.OwnerID, actionCreated, map[string]string{"name": row.Name, "slug": row.Slug})
	})
	return ws, translate(err)
}

func (r postgresRepository) Get(ctx context.Context, id, userID uuid.UUID) (Workspace, error) {
	row, err := store.New(r.pool).GetWorkspaceForMember(ctx, store.GetWorkspaceForMemberParams{ID: id, UserID: userID})
	if err != nil {
		return Workspace{}, translate(err)
	}
	return memberRow(row), nil
}

func (r postgresRepository) List(ctx context.Context, userID uuid.UUID, afterSlug string, limit int) ([]Workspace, error) {
	rows, err := store.New(r.pool).ListWorkspacesForMember(ctx, store.ListWorkspacesForMemberParams{
		UserID: userID, AfterSlug: afterSlug, RowLimit: int32(min(limit, 1000)), //nolint:gosec // bounded just above
	})
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, len(rows))
	for i, row := range rows {
		out[i] = memberRow(store.GetWorkspaceForMemberRow(row))
	}
	return out, nil
}

type change struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (r postgresRepository) Update(ctx context.Context, id, actorID uuid.UUID, in UpdateInput) (Workspace, error) {
	var ws Workspace
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockWorkspace(ctx, id)
		if err != nil {
			return err
		}
		changes := map[string]change{}
		name, slug := cur.Name, cur.Slug
		if in.Name != nil && *in.Name != cur.Name {
			name = *in.Name
			changes["name"] = change{From: cur.Name, To: name}
		}
		if in.Slug != nil && *in.Slug != cur.Slug {
			slug = *in.Slug
			changes["slug"] = change{From: cur.Slug, To: slug}
		}
		if len(changes) > 0 {
			if _, err := q.UpdateWorkspace(ctx, store.UpdateWorkspaceParams{ID: id, Name: name, Slug: slug}); err != nil {
				return err
			}
			if err := audit(ctx, q, id, actorID, actionUpdated, map[string]any{"changes": changes}); err != nil {
				return err
			}
		}
		row, err := q.GetWorkspaceForMember(ctx, store.GetWorkspaceForMemberParams{ID: id, UserID: actorID})
		ws = memberRow(row)
		return err
	})
	return ws, translate(err)
}

func audit(ctx context.Context, q *store.Queries, workspaceID, actorID uuid.UUID, action string, metadata any) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		WorkspaceID:  &workspaceID,
		ActorType:    "user",
		ActorID:      &actorID,
		Action:       action,
		ResourceType: "workspace",
		ResourceID:   &workspaceID,
		Metadata:     meta,
	})
}

func fromRow(row store.Workspace, role authz.Role) Workspace {
	return Workspace{
		ID: row.ID, Name: row.Name, Slug: row.Slug, Role: role,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func memberRow(row store.GetWorkspaceForMemberRow) Workspace {
	return Workspace{
		ID: row.ID, Name: row.Name, Slug: row.Slug, Role: authz.Role(row.Role),
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == slugConstraint {
		return ErrSlugTaken
	}
	return err
}
