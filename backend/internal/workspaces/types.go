// Package workspaces creates, lists, shows and updates workspaces.
package workspaces

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

// Workspace is a workspace as one member sees it: Role is that member's role.
type Workspace struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	Role      authz.Role
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is what a user supplies to create a workspace.
type CreateInput struct {
	Name string
	Slug string
}

// UpdateInput is a partial update. Nil fields are left unchanged.
type UpdateInput struct {
	Name *string
	Slug *string
}

// ListInput asks for up to Limit workspaces whose slug sorts after After.
type ListInput struct {
	After string
	Limit int
}

// ListPage is one page of workspaces. Next is the After value for the
// following page and is empty on the last page.
type ListPage struct {
	Items []Workspace
	Next  string
}

// NewWorkspace is a workspace to create, owned by OwnerID. OwnerEmail may be
// empty.
type NewWorkspace struct {
	Name       string
	Slug       string
	OwnerID    uuid.UUID
	OwnerEmail string
}

// Repository errors.
var (
	ErrNotFound  = errors.New("workspaces: not found")
	ErrSlugTaken = errors.New("workspaces: slug taken")
)

// Repository stores workspaces. Every write records its audit log entry in
// the same transaction.
type Repository interface {
	// Create stores the workspace, the owner's membership and a
	// workspace.created audit entry atomically. It returns ErrSlugTaken
	// when the slug is in use.
	Create(ctx context.Context, n NewWorkspace) (Workspace, error)
	// Get returns workspace id as member userID sees it, or ErrNotFound.
	Get(ctx context.Context, id, userID uuid.UUID) (Workspace, error)
	// List returns up to limit of userID's workspaces whose slug sorts
	// after afterSlug, ordered by slug.
	List(ctx context.Context, userID uuid.UUID, afterSlug string, limit int) ([]Workspace, error)
	// Update applies in and, when anything changed, records a
	// workspace.updated audit entry by actorID. It returns ErrNotFound or
	// ErrSlugTaken.
	Update(ctx context.Context, id, actorID uuid.UUID, in UpdateInput) (Workspace, error)
}
