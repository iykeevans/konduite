package members

import (
	"context"
	"encoding/json"
	"errors"
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
	actionRoleChanged       = "member.role_changed"
	actionRemoved           = "member.removed"
	actionJoined            = "member.joined"
	actionInvitationCreated = "invitation.created"
	actionInvitationRevoked = "invitation.revoked"

	openInvitationConstraint = "invitations_open_email_idx"
	memberUserConstraint     = "workspace_members_workspace_id_user_id_key"
	memberEmailConstraint    = "workspace_members_email_idx"

	maxRows = 1000
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository returns a Repository backed by Postgres.
func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return postgresRepository{pool: pool}
}

func (r postgresRepository) ListMembers(ctx context.Context, ws uuid.UUID, after MemberCursor, limit int) ([]Member, error) {
	rows, err := store.New(r.pool).ListMembers(ctx, store.ListMembersParams{
		WorkspaceID: ws, AfterJoinedAt: after.JoinedAt, AfterUserID: after.UserID,
		RowLimit: int32(min(limit, maxRows)), //nolint:gosec // bounded by maxRows
	})
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	for i, row := range rows {
		out[i] = member(row.UserID, row.Role, row.Email, row.JoinedAt)
	}
	return out, nil
}

// lockMembers locks every membership row of the workspace, so the owner
// count it returns stays true until the transaction ends.
func lockMembers(ctx context.Context, q *store.Queries, ws, actorID, userID uuid.UUID) (actor, target Member, owners int, err error) {
	rows, err := q.LockMembers(ctx, ws)
	if err != nil {
		return Member{}, Member{}, 0, err
	}
	var foundActor, foundTarget bool
	for _, row := range rows {
		m := member(row.UserID, row.Role, row.Email, row.JoinedAt)
		if m.Role == authz.RoleOwner {
			owners++
		}
		if m.UserID == actorID {
			actor, foundActor = m, true
		}
		if m.UserID == userID {
			target, foundTarget = m, true
		}
	}
	if !foundActor || !foundTarget {
		return Member{}, Member{}, 0, ErrMemberNotFound
	}
	return actor, target, owners, nil
}

type roleChange struct {
	UserID uuid.UUID `json:"user_id"`
	From   string    `json:"from"`
	To     string    `json:"to"`
}

func (r postgresRepository) ChangeRole(ctx context.Context, c RoleChange) (Member, error) {
	var out Member
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		actor, target, owners, err := lockMembers(ctx, q, c.WorkspaceID, c.ActorID, c.UserID)
		if err != nil {
			return err
		}
		if err := CheckRoleChange(actor.Role, target.Role, c.Role, owners); err != nil {
			return err
		}
		if target.Role == c.Role {
			out = target
			return nil
		}
		row, err := q.UpdateMemberRole(ctx, store.UpdateMemberRoleParams{WorkspaceID: c.WorkspaceID, UserID: c.UserID, Role: string(c.Role)})
		if err != nil {
			return err
		}
		out = member(row.UserID, row.Role, row.Email, row.JoinedAt)
		return audit(ctx, q, c.WorkspaceID, c.ActorID, actionRoleChanged, "member", c.UserID,
			roleChange{UserID: c.UserID, From: string(target.Role), To: string(c.Role)})
	})
	return out, translate(err)
}

func (r postgresRepository) RemoveMember(ctx context.Context, rm Removal) error {
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		actor, target, owners, err := lockMembers(ctx, q, rm.WorkspaceID, rm.ActorID, rm.UserID)
		if err != nil {
			return err
		}
		if err := CheckRemoval(actor.Role, target.Role, owners, rm.ActorID == rm.UserID); err != nil {
			return err
		}
		if err := q.DeleteMember(ctx, store.DeleteMemberParams{WorkspaceID: rm.WorkspaceID, UserID: rm.UserID}); err != nil {
			return err
		}
		return audit(ctx, q, rm.WorkspaceID, rm.ActorID, actionRemoved, "member", rm.UserID,
			map[string]any{"user_id": rm.UserID, "role": target.Role, "self": rm.ActorID == rm.UserID})
	})
	return translate(err)
}

func (r postgresRepository) CreateInvitation(ctx context.Context, n NewInvitation) (Invitation, error) {
	var out Invitation
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		exists, err := q.MemberEmailExists(ctx, store.MemberEmailExistsParams{WorkspaceID: n.WorkspaceID, Email: &n.Email})
		if err != nil {
			return err
		}
		if exists {
			return ErrAlreadyMember
		}
		if err := q.DeleteExpiredInvitation(ctx, store.DeleteExpiredInvitationParams{WorkspaceID: n.WorkspaceID, Email: n.Email}); err != nil {
			return err
		}
		row, err := q.InsertInvitation(ctx, store.InsertInvitationParams{
			WorkspaceID: n.WorkspaceID, Email: n.Email, Role: string(n.Role),
			TokenHash: n.TokenHash, InvitedBy: n.InvitedBy, ExpiresAt: n.ExpiresAt,
		})
		if err != nil {
			return err
		}
		out = invitation(row)
		return audit(ctx, q, n.WorkspaceID, n.InvitedBy, actionInvitationCreated, "invitation", row.ID,
			map[string]any{"email": row.Email, "role": row.Role, "expires_at": row.ExpiresAt})
	})
	return out, translate(err)
}

func (r postgresRepository) ListInvitations(ctx context.Context, ws uuid.UUID, after InvitationCursor, limit int) ([]Invitation, error) {
	rows, err := store.New(r.pool).ListOpenInvitations(ctx, store.ListOpenInvitationsParams{
		WorkspaceID: ws, AfterCreatedAt: after.CreatedAt, AfterID: after.ID,
		RowLimit: int32(min(limit, maxRows)), //nolint:gosec // bounded by maxRows
	})
	if err != nil {
		return nil, err
	}
	out := make([]Invitation, len(rows))
	for i, row := range rows {
		out[i] = invitation(row)
	}
	return out, nil
}

func (r postgresRepository) RevokeInvitation(ctx context.Context, ws, id, actorID uuid.UUID) error {
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.DeleteOpenInvitation(ctx, store.DeleteOpenInvitationParams{ID: id, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotFound
		}
		if err != nil {
			return err
		}
		return audit(ctx, q, ws, actorID, actionInvitationRevoked, "invitation", row.ID,
			map[string]any{"email": row.Email, "role": row.Role})
	})
	return translate(err)
}

func (r postgresRepository) AcceptInvitation(ctx context.Context, a Acceptance) (Joined, error) {
	var out Joined
	err := db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		inv, err := q.LockInvitationByTokenHash(ctx, a.TokenHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationNotFound
		}
		if err != nil {
			return err
		}
		if inv.AcceptedAt != nil || !inv.ExpiresAt.After(time.Now()) {
			return ErrInvitationNotFound
		}
		if inv.Email != a.Email {
			return ErrEmailMismatch
		}
		if err := q.InsertWorkspaceMember(ctx, store.InsertWorkspaceMemberParams{
			WorkspaceID: inv.WorkspaceID, UserID: a.UserID, Role: inv.Role, Email: &a.Email,
		}); err != nil {
			return err
		}
		if err := q.MarkInvitationAccepted(ctx, store.MarkInvitationAcceptedParams{ID: inv.ID, AcceptedBy: &a.UserID}); err != nil {
			return err
		}
		out = Joined{WorkspaceID: inv.WorkspaceID, Role: authz.Role(inv.Role)}
		return audit(ctx, q, inv.WorkspaceID, a.UserID, actionJoined, "member", a.UserID,
			map[string]any{"invitation_id": inv.ID, "invited_by": inv.InvitedBy, "role": inv.Role})
	})
	return out, translate(err)
}

func audit(ctx context.Context, q *store.Queries, ws, actorID uuid.UUID, action, resourceType string, resourceID uuid.UUID, metadata any) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, store.InsertAuditLogParams{
		WorkspaceID:  &ws,
		ActorType:    "user",
		ActorID:      &actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   &resourceID,
		Metadata:     meta,
	})
}

func member(userID uuid.UUID, role string, email *string, joinedAt time.Time) Member {
	m := Member{UserID: userID, Role: authz.Role(role), JoinedAt: joinedAt}
	if email != nil {
		m.Email = *email
	}
	return m
}

func invitation(row store.Invitation) Invitation {
	return Invitation{
		ID: row.ID, WorkspaceID: row.WorkspaceID, Email: row.Email, Role: authz.Role(row.Role),
		InvitedBy: row.InvitedBy, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt,
	}
}

func translate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case openInvitationConstraint:
			return ErrInvitationPending
		case memberUserConstraint, memberEmailConstraint:
			return ErrAlreadyMember
		}
	}
	return err
}
