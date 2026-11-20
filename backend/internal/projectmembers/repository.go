package projectmembers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

const (
	actionAdded       = "project.member_added"
	actionRoleChanged = "project.member_role_changed"
	actionRemoved     = "project.member_removed"

	memberConstraint = "project_members_project_id_user_id_key"

	maxRows = 1000
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) List(ctx context.Context, projectID uuid.UUID, after Cursor, limit int) ([]Member, error) {
	rows, err := store.New(r.pool).ListProjectMembers(ctx, store.ListProjectMembersParams{
		ProjectID: projectID, AfterJoinedAt: after.JoinedAt, AfterUserID: after.UserID,
		RowLimit: int32(min(limit, maxRows)), //nolint:gosec // bounded by maxRows
	})
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	for i, row := range rows {
		out[i] = member(row.UserID, row.Role, row.JoinedAt, row.Email, &row.WorkspaceRole)
	}
	return out, nil
}

func (r postgresRepository) Add(ctx context.Context, c Change) (Member, error) {
	var out Member
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		wm, err := q.GetProjectWorkspaceMember(ctx, store.GetProjectWorkspaceMemberParams{ProjectID: c.ProjectID, UserID: c.UserID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotWorkspaceMember
		}
		if err != nil {
			return err
		}
		row, err := q.InsertProjectMember(ctx, store.InsertProjectMemberParams{ProjectID: c.ProjectID, UserID: c.UserID, Role: string(c.Role)})
		if err != nil {
			return err
		}
		out = member(row.UserID, row.Role, row.JoinedAt, wm.Email, &wm.WorkspaceRole)
		return audit(ctx, q, wm.WorkspaceID, c, actionAdded, map[string]any{"user_id": c.UserID, "role": c.Role})
	})
	return out, translate(err)
}

type locked struct {
	workspaceID   uuid.UUID
	actorImplicit bool
	target        Member
	targetActive  bool
	maintainers   int
}

// lock locks every membership row of the project, so the maintainer count
// it returns stays true until the transaction ends. Only rows of users still
// in the workspace count, since the others grant nothing.
func lock(ctx context.Context, q *store.Queries, c Change) (locked, error) {
	rows, err := q.LockProjectMembers(ctx, c.ProjectID)
	if err != nil {
		return locked{}, err
	}
	var l locked
	found := false
	for _, row := range rows {
		active := row.WorkspaceRole != nil
		if active && row.Role == string(authz.ProjectRoleMaintainer) {
			l.maintainers++
		}
		if row.UserID == c.UserID {
			l.target, l.targetActive, found = member(row.UserID, row.Role, row.JoinedAt, row.Email, row.WorkspaceRole), active, true
		}
	}
	if !found {
		return locked{}, ErrMemberNotFound
	}
	actor, err := q.GetProjectWorkspaceMember(ctx, store.GetProjectWorkspaceMemberParams{ProjectID: c.ProjectID, UserID: c.ActorID})
	if errors.Is(err, pgx.ErrNoRows) {
		return locked{}, ErrMemberNotFound
	}
	if err != nil {
		return locked{}, err
	}
	l.workspaceID = actor.WorkspaceID
	l.actorImplicit = slices.Contains(authz.ImplicitMaintainers(), authz.Role(actor.WorkspaceRole))
	return l, nil
}

func (l locked) check(next authz.ProjectRole) error {
	if !l.targetActive {
		return nil
	}
	return CheckMaintainerChange(l.actorImplicit, l.target.Role, next, l.maintainers)
}

type roleChange struct {
	UserID uuid.UUID `json:"user_id"`
	From   string    `json:"from"`
	To     string    `json:"to"`
}

func (r postgresRepository) ChangeRole(ctx context.Context, c Change) (Member, error) {
	var out Member
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		l, err := lock(ctx, q, c)
		if err != nil {
			return err
		}
		if err := l.check(c.Role); err != nil {
			return err
		}
		out = l.target
		if l.target.Role == c.Role {
			return nil
		}
		if _, err := q.UpdateProjectMemberRole(ctx, store.UpdateProjectMemberRoleParams{
			ProjectID: c.ProjectID, UserID: c.UserID, Role: string(c.Role),
		}); err != nil {
			return err
		}
		out.Role = c.Role
		return audit(ctx, q, l.workspaceID, c, actionRoleChanged,
			roleChange{UserID: c.UserID, From: string(l.target.Role), To: string(c.Role)})
	})
	return out, translate(err)
}

func (r postgresRepository) Remove(ctx context.Context, c Change) error {
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		l, err := lock(ctx, q, c)
		if err != nil {
			return err
		}
		if err := l.check(""); err != nil {
			return err
		}
		if err := q.DeleteProjectMember(ctx, store.DeleteProjectMemberParams{ProjectID: c.ProjectID, UserID: c.UserID}); err != nil {
			return err
		}
		return audit(ctx, q, l.workspaceID, c, actionRemoved,
			map[string]any{"user_id": c.UserID, "role": l.target.Role, "self": c.ActorID == c.UserID})
	})
	return translate(err)
}

func audit(ctx context.Context, q *store.Queries, workspaceID uuid.UUID, c Change, action string, metadata any) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		WorkspaceID:  &workspaceID,
		ActorType:    "user",
		ActorID:      &c.ActorID,
		Action:       action,
		ResourceType: "project",
		ResourceID:   &c.ProjectID,
		Metadata:     meta,
	})
}

func member(userID uuid.UUID, role string, joinedAt time.Time, email, workspaceRole *string) Member {
	m := Member{UserID: userID, Role: authz.ProjectRole(role), JoinedAt: joinedAt}
	if email != nil {
		m.Email = *email
	}
	if workspaceRole != nil {
		m.WorkspaceRole = authz.Role(*workspaceRole)
	}
	return m
}

func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == memberConstraint {
		return ErrAlreadyMember
	}
	return err
}
