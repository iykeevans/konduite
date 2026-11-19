// Package runtimes registers local runtime connectors and issues the
// short-lived tokens they use to talk to the API.
package runtimes

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
)

// AuthorizeRegister returns nil if p may register a runtime in workspaceID,
// and an errs error (NotFound, Forbidden) to deny. Its error goes to the
// client unchanged.
type AuthorizeRegister func(ctx context.Context, p auth.Principal, workspaceID uuid.UUID) error

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

// Runtime is a stored runtime.
type Runtime struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
	Name        string
	CreatedAt   time.Time
}

// Credential is a stored refresh credential with the runtime it belongs to.
type Credential struct {
	ID          uuid.UUID
	RuntimeID   uuid.UUID
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
	SecretHash  []byte
}

// ErrCredentialNotFound is returned by Repository.Credential for an unknown id.
var ErrCredentialNotFound = errors.New("runtimes: credential not found")

// Repository stores runtimes and their credentials.
type Repository interface {
	// Create stores the runtime and its credential atomically and returns
	// the runtime and the credential's id.
	Create(ctx context.Context, n NewRuntime) (Runtime, uuid.UUID, error)
	Credential(ctx context.Context, id uuid.UUID) (Credential, error)
	TouchCredential(ctx context.Context, id uuid.UUID) error
	// DeleteCredential removes a credential. Deleting one that is already
	// gone is not an error.
	DeleteCredential(ctx context.Context, id uuid.UUID) error
}
