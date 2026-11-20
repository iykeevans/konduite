// Package runtimes registers local runtime connectors and issues the
// short-lived tokens they use to talk to the API.
package runtimes

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
)

// Authorizer is the authz.Authorizer method the service needs.
type Authorizer interface {
	Can(ctx context.Context, p auth.Principal, action authz.Action, r authz.Resource) error
}

// RegisterInput is what a user supplies to register a runtime.
type RegisterInput struct {
	WorkspaceID uuid.UUID
	Name        string
}

// Registration is a newly registered runtime. RefreshCredential is only ever
// available here: the server keeps just its hash.
type Registration struct {
	RuntimeID         uuid.UUID
	WorkspaceID       uuid.UUID
	OwnerID           uuid.UUID
	Name              string
	RefreshCredential string
	CreatedAt         time.Time
}

// AccessToken is a signed runtime JWT.
type AccessToken struct {
	Token     string
	ExpiresAt time.Time
}

// Claims are the verified contents of a runtime access token.
type Claims struct {
	RuntimeID   uuid.UUID
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
	ExpiresAt   time.Time
}

// NewRuntime is a runtime and its first credential, stored together.
type NewRuntime struct {
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
	Name        string
	SecretHash  []byte
}

// Runtime is a stored runtime. It never carries credential material: Revoked
// says the runtime has no refresh credential left (it logged out or was
// revoked), and CredentialLastUsedAt is the last token exchange, if any.
type Runtime struct {
	ID                   uuid.UUID
	WorkspaceID          uuid.UUID
	OwnerID              uuid.UUID
	Name                 string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	CredentialLastUsedAt *time.Time
	Revoked              bool
}

// Position is where a page of runtimes ends: the last item's creation time
// and id, in newest-first order.
type Position struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"id"`
}

// ListInput asks for a page of a workspace's runtimes, newest first.
type ListInput struct {
	WorkspaceID uuid.UUID
	Limit       int
	After       *Position
}

// Page is one page of runtimes. Next is nil on the last page.
type Page struct {
	Items []Runtime
	Next  *Position
}

// ListFilter selects at most Limit runtimes of a workspace, newest first,
// strictly after After.
type ListFilter struct {
	WorkspaceID uuid.UUID
	After       *Position
	Limit       int
}

// Credential is a stored refresh credential with the runtime it belongs to.
type Credential struct {
	ID          uuid.UUID
	RuntimeID   uuid.UUID
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
	SecretHash  []byte
}

var (
	// ErrNotFound is returned by Repository.Get for an unknown runtime.
	ErrNotFound = errors.New("runtimes: not found")
	// ErrCredentialNotFound is returned by Repository.Credential for an
	// unknown id.
	ErrCredentialNotFound = errors.New("runtimes: credential not found")
)

// Repository stores runtimes and their credentials.
type Repository interface {
	// Create stores the runtime and its credential atomically and returns
	// the runtime and the credential's id.
	Create(ctx context.Context, n NewRuntime) (Runtime, uuid.UUID, error)
	Get(ctx context.Context, id uuid.UUID) (Runtime, error)
	List(ctx context.Context, f ListFilter) ([]Runtime, error)
	Credential(ctx context.Context, id uuid.UUID) (Credential, error)
	TouchCredential(ctx context.Context, id uuid.UUID) error
	// DeleteCredential removes a credential. Deleting one that is already
	// gone is not an error.
	DeleteCredential(ctx context.Context, id uuid.UUID) error
}
