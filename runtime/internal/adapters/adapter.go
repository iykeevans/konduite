package adapters

import (
	"context"
	"errors"
)

// ErrMissingMessageID is returned for input without a message id. Dedupe and
// invocation counting key on that id, so an adapter must not run without one.
var ErrMissingMessageID = errors.New("adapters: input has no message id")

// AgentInput is one delivered message handed to a local agent.
// TODO(ADP-001): replace with the delivery payload type once packages/contracts has one.
type AgentInput struct {
	MessageID      string
	ConversationID string
	Content        string
}

// Reply is what the agent sends back to the conversation. InReplyTo carries
// the input's message id so the runtime can tell replies from new messages.
type Reply struct {
	InReplyTo      string
	ConversationID string
	Content        string
}

// Adapter runs messages through one local agent provider. Provider matches
// AnnouncedAgent.Provider in runtime.hello.
//
// Handle may be called concurrently and more than once for the same message
// (at-least-once delivery); deduping is the caller's job, not the adapter's.
type Adapter interface {
	Provider() string
	Handle(ctx context.Context, in AgentInput) (Reply, error)
}
