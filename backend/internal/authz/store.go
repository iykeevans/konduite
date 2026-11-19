package authz

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iykeevans/konduite/backend/internal/store"
)

// StoreMemberships returns Memberships backed by the workspace_members table.
// It also implements ProjectMemberships from projects and project_members.
func StoreMemberships(q *store.Queries) Memberships {
	return storeMemberships{q: q}
}

var _ ProjectMemberships = storeMemberships{}

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

func (s storeMemberships) ProjectMembership(ctx context.Context, projectID, userID uuid.UUID) (ProjectMembership, error) {
	row, err := s.q.GetProjectMembership(ctx, store.GetProjectMembershipParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMembership{}, ErrNotMember
	}
	if err != nil {
		return ProjectMembership{}, err
	}
	return ProjectMembership{
		WorkspaceID:   row.WorkspaceID,
		WorkspaceRole: Role(row.WorkspaceRole),
		ProjectRole:   ProjectRole(row.ProjectRole),
	}, nil
}
