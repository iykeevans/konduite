package messages

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

// Conversations resolves a conversation and checks the caller's right to it.
// *conversations.Service satisfies it.
type Conversations interface {
	// Get returns the conversation if p may read it.
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (conversations.Conversation, error)
	// Changeable returns the conversation if p may contribute to it.
	Changeable(ctx context.Context, p auth.Principal, id uuid.UUID) (conversations.Conversation, error)
}

// PostInput is what a user sends. An empty Type is text; an empty
// IdempotencyKey gets a fresh one, so such a post cannot be deduplicated.
type PostInput struct {
	IdempotencyKey string
	ParentID       *uuid.UUID
	Type           Type
	Content        string
	Metadata       json.RawMessage
}

// ListInput asks for a page of messages after AfterSeq, in sequence order.
type ListInput struct {
	AfterSeq int64
	Limit    int
}

// Page is a page of messages. Next is the sequence number to pass as
// AfterSeq for the following page, nil on the last page.
type Page struct {
	Items []Message
	Next  *int64
}

// Service posts and lists a conversation's messages, checking authorization
// on every call.
type Service struct {
	repo  Repository
	convs Conversations
}

// NewService returns a Service backed by repo.
func NewService(repo Repository, convs Conversations) *Service {
	return &Service{repo: repo, convs: convs}
}

// Post stores a message from p in the conversation. p must be a user who may
// contribute to it; p becomes a participant if not one already. created is
// false when p already posted with the same idempotency key, and the original
// message is returned unchanged.
func (s *Service) Post(ctx context.Context, p auth.Principal, conversationID uuid.UUID, in PostInput) (Message, bool, error) {
	if in.Type == "" {
		in.Type = TypeText
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = uuid.NewString()
	}
	n := NewMessage{
		ConversationID: conversationID,
		Sender:         Sender{Kind: conversations.KindUser, ID: p.ID},
		IdempotencyKey: in.IdempotencyKey,
		ParentID:       in.ParentID,
		Type:           in.Type,
		Content:        in.Content,
		Metadata:       in.Metadata,
	}
	err := n.Validate()
	if in.Type == TypeSystem {
		err = withField(err, "message_type", "must be text or task")
	}
	if err != nil {
		return Message{}, false, err
	}
	notFound := errs.NotFound("conversation not found")
	if p.Kind != auth.KindUser {
		return Message{}, false, notFound
	}
	c, err := s.convs.Changeable(ctx, p, conversationID)
	if err != nil {
		return Message{}, false, err
	}
	m, created, err := s.repo.Post(ctx, Posting{Message: n, WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID})
	switch {
	case errors.Is(err, ErrConversationNotFound):
		return Message{}, false, notFound
	case errors.Is(err, ErrParentNotFound):
		return Message{}, false, errs.Invalid("invalid message", map[string]string{"parent_id": "not found in this conversation"})
	}
	return m, created, err
}

// List returns a page of the conversation's messages in sequence order. p
// must be able to read the conversation.
func (s *Service) List(ctx context.Context, p auth.Principal, conversationID uuid.UUID, in ListInput) (Page, error) {
	if in.AfterSeq < 0 {
		return Page{}, errs.Invalid("invalid message query", map[string]string{"after_seq": "must be zero or more"})
	}
	if _, err := s.convs.Get(ctx, p, conversationID); err != nil {
		return Page{}, err
	}
	items, err := s.repo.List(ctx, ListFilter{ConversationID: conversationID, AfterSeq: in.AfterSeq, Limit: in.Limit + 1})
	if err != nil {
		return Page{}, err
	}
	if len(items) <= in.Limit {
		return Page{Items: items}, nil
	}
	items = items[:in.Limit]
	next := items[len(items)-1].SequenceNumber
	return Page{Items: items, Next: &next}, nil
}

func withField(err error, field, msg string) error {
	fields := map[string]string{}
	if e, ok := errs.As(err); ok && e.Kind == errs.KindInvalid {
		fields = e.Fields
	}
	fields[field] = msg
	return errs.Invalid("invalid message", fields)
}
