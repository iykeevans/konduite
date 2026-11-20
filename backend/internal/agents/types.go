// Package agents keeps the agents each runtime announces. An agent has its own
// identity, separate from the runtime that hosts it.
package agents

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/packages/contracts"
)

// Status is an agent's availability.
type Status string

// Agent statuses. SyncFromHello only sets online and offline; busy is for
// running executions (AGENT-004).
const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
	StatusBusy    Status = "busy"
)

// Runtime is the authenticated runtime an announcement came from. Both ids
// must come from its verified access token, never from the frame.
type Runtime struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
}

// Announcement is one agent from a runtime.hello frame.
type Announcement struct {
	Provider     string
	Name         string
	Version      string
	Capabilities contracts.AgentCapabilities
}

// Agent is a stored agent. (RuntimeID, Provider, Name) identifies it.
type Agent struct {
	ID           uuid.UUID
	WorkspaceID  uuid.UUID
	RuntimeID    uuid.UUID
	Provider     string
	Name         string
	Version      string
	Capabilities contracts.AgentCapabilities
	DisplayName  string
	Description  string
	Status       Status
	LastSeenAt   time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrRuntimeNotFound is returned by Repository.Sync when the runtime no longer
// exists in the given workspace, for example because it was revoked.
var ErrRuntimeNotFound = errors.New("agents: runtime not found")

// Repository stores agents and their project grants.
type Repository interface {
	// Sync atomically upserts every announced agent as online and marks the
	// runtime's other agents offline. It returns the announced agents in order.
	Sync(ctx context.Context, rt Runtime, announced []Announcement) ([]Agent, error)
	HasProjectAccess(ctx context.Context, agentID, projectID uuid.UUID) (bool, error)
}

// Authorizer is the authz.Authorizer method the registry needs.
type Authorizer interface {
	Can(ctx context.Context, p auth.Principal, action authz.Action, r authz.Resource) error
}

// RuntimeOwner is a runtime with the user who registered it.
type RuntimeOwner struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	OwnerID     uuid.UUID
}

// RegisterInput describes an agent registered ahead of its runtime announcing
// it. Provider and Name must match what the runtime will announce, since
// (runtime, provider, name) is the agent's identity.
type RegisterInput struct {
	Provider     string
	Name         string
	DisplayName  string
	Description  string
	Capabilities contracts.AgentCapabilities
}

// UpdateInput changes an agent's labels. A nil field is left alone and an
// empty string clears it.
type UpdateInput struct {
	DisplayName *string
	Description *string
}

// Actor is who made a change, for the audit log.
type Actor struct {
	Type string
	ID   uuid.UUID
}

// Audit actor types.
const (
	ActorUser    = "user"
	ActorRuntime = "runtime"
)

// NewAgent is an agent to store for rt.
type NewAgent struct {
	Runtime Runtime
	RegisterInput
	Actor Actor
}

// Position is where a page of agents ends, in newest-first order.
type Position struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"id"`
}

// ListInput asks for a page of a workspace's agents, newest first,
// optionally only those on one runtime.
type ListInput struct {
	WorkspaceID uuid.UUID
	RuntimeID   uuid.UUID
	Limit       int
	After       *Position
}

// Page is one page of agents. Next is nil on the last page.
type Page struct {
	Items []Agent
	Next  *Position
}

// ListFilter selects at most Limit agents strictly after After.
type ListFilter struct {
	WorkspaceID uuid.UUID
	RuntimeID   uuid.UUID
	After       *Position
	Limit       int
}

var (
	// ErrNotFound is returned for an agent that does not exist.
	ErrNotFound = errors.New("agents: not found")
	// ErrAgentExists is returned by RegistryRepository.Create when the
	// runtime already has an agent with that provider and name.
	ErrAgentExists = errors.New("agents: already exists")
)

// RegistryRepository stores agents registered and managed through the API.
type RegistryRepository interface {
	Runtime(ctx context.Context, id uuid.UUID) (RuntimeOwner, error)
	// Create stores a new offline agent. It returns ErrRuntimeNotFound when
	// the runtime is gone or not in n.Runtime.WorkspaceID.
	Create(ctx context.Context, n NewAgent) (Agent, error)
	Get(ctx context.Context, id uuid.UUID) (Agent, error)
	List(ctx context.Context, f ListFilter) ([]Agent, error)
	RuntimeAgents(ctx context.Context, runtimeID uuid.UUID) ([]Agent, error)
	Update(ctx context.Context, id uuid.UUID, actor Actor, in UpdateInput) (Agent, error)
	// Delete removes the agent and, by cascade, its project grants.
	Delete(ctx context.Context, id uuid.UUID, actor Actor) error
}
