package messages

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/packages/contracts"
)

// Validate reports every problem with n as an errs.Invalid keyed by the
// request field name.
func (n NewMessage) Validate() error {
	fields := map[string]string{}
	if n.ConversationID == uuid.Nil {
		fields["conversation_id"] = "required"
	}
	switch n.Sender.Kind {
	case conversations.KindUser, conversations.KindAgent, conversations.KindRuntime:
	default:
		fields["sender.type"] = "must be user, agent or runtime"
	}
	if n.Sender.ID == uuid.Nil {
		fields["sender.id"] = "required"
	}
	switch l := utf8.RuneCountInString(n.IdempotencyKey); {
	case l == 0:
		fields["idempotency_key"] = "required"
	case l > MaxIdempotencyKeyLen:
		fields["idempotency_key"] = "must be at most 255 characters"
	}
	if n.ParentID != nil && *n.ParentID == uuid.Nil {
		fields["parent_id"] = "must be a message id"
	}
	switch n.Type {
	case TypeText, TypeTask, TypeSystem:
	default:
		fields["message_type"] = "must be text, task or system"
	}
	switch {
	case strings.TrimSpace(n.Content) == "":
		fields["content"] = "required"
	case utf8.RuneCountInString(n.Content) > MaxContentLen:
		fields["content"] = "must be at most 65536 characters"
	}
	if n.Metadata != nil && !isObject(n.Metadata) {
		fields["metadata"] = "must be a JSON object"
	}
	if len(fields) > 0 {
		return errs.Invalid("invalid message", fields)
	}
	return nil
}

func isObject(raw json.RawMessage) bool {
	return json.Valid(raw) && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{"))
}

// CreatedEvent is the message.created event for m, to enqueue in the
// transaction that stored it. It leaves out the content and metadata:
// consumers load the message by id.
func CreatedEvent(m Message, workspaceID uuid.UUID, projectID *uuid.UUID) events.Event {
	data := contracts.MessageCreated{
		MessageID:      m.ID.String(),
		ConversationID: m.ConversationID.String(),
		SequenceNumber: int(m.SequenceNumber),
		Sender: contracts.MessageSender{
			Type: contracts.MessageCreatedSenderType(m.Sender.Kind),
			ID:   m.Sender.ID.String(),
		},
		MessageType: contracts.MessageCreatedMessageType(m.Type),
	}
	if m.ParentID != nil {
		p := m.ParentID.String()
		data.ParentID = &p
	}
	return events.Event{
		Type:        "message.created",
		Version:     1,
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		AggregateID: m.ID,
		Data:        data,
	}
}
