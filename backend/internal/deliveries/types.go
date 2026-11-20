// Package deliveries fans a stored message out to its recipients. The router
// consumes message.created, writes one message_deliveries row per recipient
// and wakes each recipient runtime with a runtime.deliver event on
// agenthub.rt.<runtime_id>.deliver. Pushing the deliveries over the
// runtime's connection, and every status after pending, belong to MSG-008.
package deliveries

import (
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/conversations"
)

// Sender is the participant that wrote the message being routed.
type Sender struct {
	Kind conversations.ParticipantKind
	ID   uuid.UUID
}

// Participant is a conversation participant as routing sees it. RuntimeID is
// the agent's runtime for an agent, the runtime itself for a runtime, and
// nil for a user or for an agent or runtime that no longer exists in the
// conversation's workspace.
type Participant struct {
	Kind      conversations.ParticipantKind
	ID        uuid.UUID
	RuntimeID *uuid.UUID
}

// Recipient gets a delivery row. RuntimeID is nil for a user, who has no
// runtime to wake.
type Recipient struct {
	Kind      conversations.ParticipantKind
	ID        uuid.UUID
	RuntimeID *uuid.UUID
}
