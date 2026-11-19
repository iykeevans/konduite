package authz

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iykeevans/konduite/backend/internal/store"
)

// StoreMemberships returns Memberships backed by the workspace_members table.
func StoreMemberships(q *store.Queries) Memberships {
	return storeMemberships{q: q}
}

type storeMemberships struct {
	q *store.Queries
}

func (s storeMemberships) Role(ctx context.Context, workspaceID, userID uuid.UUID) (Role, error) {
	role, err := s.q.GetWorkspaceMemberRole(ctx, store.GetWorkspaceMemberRoleParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotMember
	}
	if err != nil {
		return "", err
	}
	return Role(role), nil
}
