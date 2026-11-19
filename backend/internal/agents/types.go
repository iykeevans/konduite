// Package agents keeps the agents each runtime announces. An agent has its own
// identity, separate from the runtime that hosts it.
package agents

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

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
