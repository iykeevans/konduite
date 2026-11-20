// Package messages stores the messages of a conversation. Each message takes
// the next gapless sequence number of its conversation, and a retried write
// with the same sender and idempotency key returns the original message.
package messages

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/conversations"
)

// Type says what a message is for.
type Type string

// Message types, as stored in messages.message_type.
const (
	// TypeText is conversation between people and agents.
	TypeText Type = "text"
	// TypeTask asks an agent to do work; executions hang off it.
	TypeTask Type = "task"
	// TypeSystem is written by the platform, e.g. execution status lines.
	TypeSystem Type = "system"
)

// MaxContentLen is the longest content in characters. The migration's
// CHECK holds the same limit.
const MaxContentLen = 65536

// MaxIdempotencyKeyLen is the longest idempotency key in characters.
const MaxIdempotencyKeyLen = 255

// Sender is the participant that wrote a message.
type Sender struct {
	Kind conversations.ParticipantKind
	ID   uuid.UUID
}

// NewMessage is a message to store. Metadata may be nil, which stores {}.
type NewMessage struct {
	ConversationID uuid.UUID
	Sender         Sender
	IdempotencyKey string
	ParentID       *uuid.UUID
	Type           Type
	Content        string
	Metadata       json.RawMessage
}

// Message is a stored message.
type Message struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	SequenceNumber int64
	Sender         Sender
	IdempotencyKey string
	ParentID       *uuid.UUID
	Type           Type
	Content        string
	Metadata       json.RawMessage
	CreatedAt      time.Time
}

var (
	// ErrNotFound is returned by Repository.Get for an unknown id.
	ErrNotFound = errors.New("messages: not found")
	// ErrConversationNotFound is returned when appending to a conversation
	// that does not exist.
	ErrConversationNotFound = errors.New("messages: conversation not found")
	// ErrParentNotFound is returned when the parent message does not exist in
	// the same conversation.
	ErrParentNotFound = errors.New("messages: parent not found in conversation")
)

// Repository stores messages.
type Repository interface {
	// Append stores n in its own transaction; see AppendTx.
	Append(ctx context.Context, n NewMessage) (m Message, created bool, err error)
	Get(ctx context.Context, id uuid.UUID) (Message, error)
}
