// Package projectmembers lists and manages who belongs to a project and in
// which project role. Only members of the project's workspace can be added.
package projectmembers

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

// Member is a user's explicit membership of a project. Email is the address
// they joined the workspace with and may be empty. WorkspaceRole is their
// role in the project's workspace.
type Member struct {
	UserID        uuid.UUID
	Role          authz.ProjectRole
	Email         string
	WorkspaceRole authz.Role
	JoinedAt      time.Time
}

// Cursor is a position in a member list ordered by join time then user id.
// The zero value starts at the beginning.
type Cursor struct {
	JoinedAt time.Time
	UserID   uuid.UUID
}

// Page is one page of members. Next is nil on the last page.
type Page struct {
	Items []Member
	Next  *Cursor
}

// Change asks the repository to add UserID with Role, give them Role, or
// remove them, on behalf of ActorID.
type Change struct {
	ProjectID uuid.UUID
	ActorID   uuid.UUID
	UserID    uuid.UUID
	Role      authz.ProjectRole
}

// Repository errors.
var (
	ErrMemberNotFound     = errors.New("projectmembers: member not found")
	ErrAlreadyMember      = errors.New("projectmembers: already a member")
	ErrNotWorkspaceMember = errors.New("projectmembers: user is not a member of the workspace")
)

// Repository stores project memberships. Every write records its audit log
// entry in the same transaction. Role changes and removals lock the
// project's memberships and apply CheckMaintainerChange inside that
// transaction, so two concurrent changes cannot leave the project without a
// maintainer.
type Repository interface {
	// List returns up to limit members after the cursor, leaving out users
	// who are no longer in the project's workspace.
	List(ctx context.Context, projectID uuid.UUID, after Cursor, limit int) ([]Member, error)
	// Add returns ErrNotWorkspaceMember or ErrAlreadyMember.
	Add(ctx context.Context, c Change) (Member, error)
	// ChangeRole returns ErrMemberNotFound or a CheckMaintainerChange error.
	ChangeRole(ctx context.Context, c Change) (Member, error)
	// Remove ignores c.Role. It returns ErrMemberNotFound or a
	// CheckMaintainerChange error.
	Remove(ctx context.Context, c Change) error
}
