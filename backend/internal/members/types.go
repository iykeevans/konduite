// Package members lists and manages workspace members and the invitations
// that add new ones.
package members

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

// Member is one user's membership of a workspace. Email is the address they
// joined with and may be empty.
type Member struct {
	UserID   uuid.UUID
	Role     authz.Role
	Email    string
	JoinedAt time.Time
}

// Invitation is an open or accepted invitation. The token is never stored,
// only its hash.
type Invitation struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Email       string
	Role        authz.Role
	InvitedBy   uuid.UUID
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// IssuedInvitation is a new invitation with the raw token, which is returned
// to the inviter once and cannot be recovered later.
type IssuedInvitation struct {
	Invitation
	Token string
}

// Joined is the membership an accepted invitation created.
type Joined struct {
	WorkspaceID uuid.UUID
	Role        authz.Role
}

// MemberCursor is a position in a member list ordered by join time then
// user id. The zero value starts at the beginning.
type MemberCursor struct {
	JoinedAt time.Time
	UserID   uuid.UUID
}

// InvitationCursor is a position in an invitation list ordered by creation
// time then id. The zero value starts at the beginning.
type InvitationCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// MemberPage is one page of members. Next is nil on the last page.
type MemberPage struct {
	Items []Member
	Next  *MemberCursor
}

// InvitationPage is one page of open invitations. Next is nil on the last page.
type InvitationPage struct {
	Items []Invitation
	Next  *InvitationCursor
}

// RoleChange asks the repository to give UserID a new role, on behalf of ActorID.
type RoleChange struct {
	WorkspaceID uuid.UUID
	ActorID     uuid.UUID
	UserID      uuid.UUID
	Role        authz.Role
}

// Removal asks the repository to remove UserID, on behalf of ActorID.
type Removal struct {
	WorkspaceID uuid.UUID
	ActorID     uuid.UUID
	UserID      uuid.UUID
}

// NewInvitation is an invitation to store.
type NewInvitation struct {
	WorkspaceID uuid.UUID
	Email       string
	Role        authz.Role
	TokenHash   string
	InvitedBy   uuid.UUID
	ExpiresAt   time.Time
}

// Acceptance asks the repository to redeem the invitation whose token hashes
// to TokenHash for the signed-in user UserID with email Email.
type Acceptance struct {
	TokenHash string
	UserID    uuid.UUID
	Email     string
}

// Repository errors.
var (
	ErrMemberNotFound     = errors.New("members: member not found")
	ErrInvitationNotFound = errors.New("members: invitation not found")
	ErrAlreadyMember      = errors.New("members: already a member")
	ErrInvitationPending  = errors.New("members: invitation already pending")
	ErrEmailMismatch      = errors.New("members: invitation is for another email")
)

// Repository stores memberships and invitations. Every write records its
// audit log entry in the same transaction. Role changes and removals lock
// the workspace's memberships and apply CheckRoleChange or CheckRemoval
// inside that transaction, so two concurrent changes cannot leave the
// workspace without an owner.
type Repository interface {
	// ListMembers returns up to limit members after the cursor.
	ListMembers(ctx context.Context, workspaceID uuid.UUID, after MemberCursor, limit int) ([]Member, error)
	// ChangeRole returns ErrMemberNotFound or a CheckRoleChange error.
	ChangeRole(ctx context.Context, c RoleChange) (Member, error)
	// RemoveMember returns ErrMemberNotFound or a CheckRemoval error.
	RemoveMember(ctx context.Context, r Removal) error
	// CreateInvitation replaces an expired open invitation for the same
	// email. It returns ErrAlreadyMember or ErrInvitationPending.
	CreateInvitation(ctx context.Context, n NewInvitation) (Invitation, error)
	// ListInvitations returns up to limit open invitations after the cursor,
	// expired ones included.
	ListInvitations(ctx context.Context, workspaceID uuid.UUID, after InvitationCursor, limit int) ([]Invitation, error)
	// RevokeInvitation deletes an open invitation by actorID, or returns
	// ErrInvitationNotFound.
	RevokeInvitation(ctx context.Context, workspaceID, invitationID, actorID uuid.UUID) error
	// AcceptInvitation adds the membership and marks the invitation used.
	// It returns ErrInvitationNotFound for an unknown, used or expired
	// token, ErrEmailMismatch or ErrAlreadyMember.
	AcceptInvitation(ctx context.Context, a Acceptance) (Joined, error)
}
