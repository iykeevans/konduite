// Package projects creates, lists, shows and updates the projects in a
// workspace.
package projects

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
)

// Authorizer is the part of *authz.Authorizer the service uses.
type Authorizer interface {
	Can(ctx context.Context, p auth.Principal, action authz.Action, r authz.Resource) error
}

// Project is a project in a workspace. RepositoryURL is empty when none is
// set.
type Project struct {
	ID            uuid.UUID
	WorkspaceID   uuid.UUID
	Name          string
	Description   string
	RepositoryURL string
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateInput is what a user supplies to create a project.
type CreateInput struct {
	Name          string
	Description   string
	RepositoryURL string
}

// UpdateInput is a partial update. Nil fields are left unchanged; an empty
// RepositoryURL clears it.
type UpdateInput struct {
	Name          *string
	Description   *string
	RepositoryURL *string
}

// ListInput asks for up to Limit projects whose name sorts after After.
type ListInput struct {
	After string
	Limit int
}

// ListPage is one page of projects. Next is the After value for the
// following page and is empty on the last page.
type ListPage struct {
	Items []Project
	Next  string
}

// NewProject is a project to create in WorkspaceID, created by CreatorID.
type NewProject struct {
	WorkspaceID   uuid.UUID
	CreatorID     uuid.UUID
	Name          string
	Description   string
	RepositoryURL string
}

// Repository errors.
var (
	ErrNotFound  = errors.New("projects: not found")
	ErrNameTaken = errors.New("projects: name taken")
)

// Repository stores projects. Every write records its audit log entry in the
// same transaction. It does no authorization: the service checks first.
type Repository interface {
	// Create stores the project, the creator's maintainer membership and a
	// project.created audit entry atomically. It returns ErrNameTaken when
	// the workspace already has a project with that name.
	Create(ctx context.Context, n NewProject) (Project, error)
	// Get returns project id or ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (Project, error)
	// List returns up to limit of the projects in workspaceID that userID
	// may view, whose name sorts after afterName, ordered by name.
	List(ctx context.Context, workspaceID, userID uuid.UUID, afterName string, limit int) ([]Project, error)
	// Update applies in and, when anything changed, records a
	// project.updated audit entry by actorID. It returns ErrNotFound or
	// ErrNameTaken.
	Update(ctx context.Context, id, actorID uuid.UUID, in UpdateInput) (Project, error)
}
