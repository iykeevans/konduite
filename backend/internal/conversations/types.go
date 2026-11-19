// Package conversations stores conversations and who takes part in them.
// Messages (MSG-002) hang off a conversation and take their sequence numbers
// from its LastSeq.
package conversations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
)

// ParticipantKind says what a participant id refers to.
type ParticipantKind string

// Participant kinds, as stored in conversation_participants.participant_type.
const (
	KindUser    ParticipantKind = "user"
	KindAgent   ParticipantKind = "agent"
	KindRuntime ParticipantKind = "runtime"
)

// Participant is a user, agent or runtime taking part in a conversation.
type Participant struct {
	Kind     ParticipantKind
	ID       uuid.UUID
	JoinedAt time.Time
}

// Conversation is a stored conversation. Participants is filled by Create and
// Get, and left nil in list results.
type Conversation struct {
	ID           uuid.UUID
	WorkspaceID  uuid.UUID
	ProjectID    *uuid.UUID
	Title        string
	CreatedBy    uuid.UUID
	LastSeq      int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Participants []Participant
}

// CreateInput is what a user supplies to start a conversation.
type CreateInput struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	Title       string
}

// Position is where a page of conversations ends: the last item's creation
// time and id, in newest-first order.
type Position struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"id"`
}

// ListInput asks for a page of a workspace's conversations, newest first.
type ListInput struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	Limit       int
	After       *Position
}

// Page is one page of conversations. Next is nil on the last page.
type Page struct {
	Items []Conversation
	Next  *Position
}

// NewConversation is a conversation to store. Its creator becomes its first
// participant.
type NewConversation struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	Title       string
	CreatedBy   uuid.UUID
}

// ListFilter selects at most Limit conversations of a workspace, newest
// first, strictly after After.
type ListFilter struct {
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	After       *Position
	Limit       int
}

var (
	// ErrNotFound is returned by Repository.Get for an unknown id.
	ErrNotFound = errors.New("conversations: not found")
	// ErrProjectNotFound is returned by Repository.Create when the project
	// does not exist in the conversation's workspace.
	ErrProjectNotFound = errors.New("conversations: project not found in workspace")
)

// Repository stores conversations and their participants.
type Repository interface {
	// Create stores the conversation and its creator as a user participant
	// in one transaction.
	Create(ctx context.Context, n NewConversation) (Conversation, error)
	// Get returns the conversation with its participants.
	Get(ctx context.Context, id uuid.UUID) (Conversation, error)
	List(ctx context.Context, f ListFilter) ([]Conversation, error)
}

// Authorizer is the authz.Authorizer method the service needs.
type Authorizer interface {
	Can(ctx context.Context, p auth.Principal, action authz.Action, r authz.Resource) error
}
