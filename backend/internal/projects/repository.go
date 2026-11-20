package projects

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
	actionCreated  = "project.created"
	actionUpdated  = "project.updated"
	nameConstraint = "projects_workspace_id_name_key"
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) Create(ctx context.Context, n NewProject) (Project, error) {
	var project Project
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.InsertProject(ctx, store.InsertProjectParams{
			WorkspaceID: n.WorkspaceID, Name: n.Name, Description: n.Description,
			RepositoryUrl: nullable(n.RepositoryURL), CreatedBy: n.CreatorID,
		})
		if err != nil {
			return err
		}
		if err := q.InsertProjectCreatorMembership(ctx, store.InsertProjectCreatorMembershipParams{
			ProjectID: row.ID, UserID: n.CreatorID,
		}); err != nil {
			return err
		}
		project = fromRow(row)
		return audit(ctx, q, project, n.CreatorID, actionCreated, map[string]string{
			"name": row.Name, "repository_url": project.RepositoryURL,
		})
	})
	return project, translate(err)
}

func (r postgresRepository) Get(ctx context.Context, id uuid.UUID) (Project, error) {
	row, err := store.New(r.pool).GetProject(ctx, id)
	if err != nil {
		return Project{}, translate(err)
	}
	return fromRow(row), nil
}

func (r postgresRepository) List(ctx context.Context, workspaceID, userID uuid.UUID, afterName string, limit int) ([]Project, error) {
	seeAll := authz.ImplicitMaintainers()
	roles := make([]string, len(seeAll))
	for i, role := range seeAll {
		roles[i] = string(role)
	}
	rows, err := store.New(r.pool).ListVisibleProjects(ctx, store.ListVisibleProjectsParams{
		UserID: userID, WorkspaceID: workspaceID, AfterName: afterName, SeeAllRoles: roles,
		RowLimit: int32(min(limit, 1000)), //nolint:gosec // bounded just above
	})
	if err != nil {
		return nil, err
	}
	out := make([]Project, len(rows))
	for i, row := range rows {
		out[i] = fromRow(row)
	}
	return out, nil
}

type change struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (r postgresRepository) Update(ctx context.Context, id, actorID uuid.UUID, in UpdateInput) (Project, error) {
	var project Project
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.LockProject(ctx, id)
		if err != nil {
			return err
		}
		cur := fromRow(row)
		next := cur
		changes := map[string]change{}
		apply := func(field string, to, dst *string) {
			if to != nil && *to != *dst {
				changes[field] = change{From: *dst, To: *to}
				*dst = *to
			}
		}
		apply("name", in.Name, &next.Name)
		apply("description", in.Description, &next.Description)
		apply("repository_url", in.RepositoryURL, &next.RepositoryURL)
		if len(changes) == 0 {
			project = cur
			return nil
		}
		row, err = q.UpdateProject(ctx, store.UpdateProjectParams{
			ID: id, Name: next.Name, Description: next.Description, RepositoryUrl: nullable(next.RepositoryURL),
		})
		if err != nil {
			return err
		}
		project = fromRow(row)
		return audit(ctx, q, project, actorID, actionUpdated, map[string]any{"changes": changes})
	})
	return project, translate(err)
}

func audit(ctx context.Context, q *store.Queries, p Project, actorID uuid.UUID, action string, metadata any) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		WorkspaceID:  &p.WorkspaceID,
		ActorType:    "user",
		ActorID:      &actorID,
		Action:       action,
		ResourceType: "project",
		ResourceID:   &p.ID,
		Metadata:     meta,
	})
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func fromRow(row store.Project) Project {
	p := Project{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, Description: row.Description,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.RepositoryUrl != nil {
		p.RepositoryURL = *row.RepositoryUrl
	}
	return p
}

func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == nameConstraint {
		return ErrNameTaken
	}
	return err
}
