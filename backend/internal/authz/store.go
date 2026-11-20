package authz

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iykeevans/konduite/backend/internal/store"
)

// StoreMemberships returns the Directory backed by workspace_members,
// projects and project_members. Pass it to NewWithProjects.
func StoreMemberships(q *store.Queries) StoreDirectory {
	return StoreDirectory{q: q}
}

// StoreDirectory is the Postgres-backed Directory.
type StoreDirectory struct {
	q *store.Queries
}

// Role implements Memberships.
func (s StoreDirectory) Role(ctx context.Context, workspaceID, userID uuid.UUID) (Role, error) {
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

// ProjectMembership implements ProjectMemberships.
func (s StoreDirectory) ProjectMembership(ctx context.Context, projectID, userID uuid.UUID) (ProjectMembership, error) {
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
