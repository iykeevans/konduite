// Package echo is the built-in adapter that replies to every message with its
// own content. It counts invocations per message id so tests can assert that
// each message reached the agent exactly once.
package echo

import (
	"context"
	"sync"

	"github.com/iykeevans/konduite/runtime/internal/adapters"
)

// Provider is the name the echo adapter announces in runtime.hello.
const Provider = "echo"

// Adapter echoes messages back. The zero value is not usable; call New.
type Adapter struct {
	mu    sync.Mutex
	calls map[string]int
	total int
}

// New returns an echo adapter with no invocations recorded.
func New() *Adapter {
	return &Adapter{calls: make(map[string]int)}
}

// Provider returns "echo".
func (a *Adapter) Provider() string { return Provider }

// Handle counts the call before checking ctx, so a duplicate dispatch shows
// up in the counts even if it was canceled.
func (a *Adapter) Handle(ctx context.Context, in adapters.AgentInput) (adapters.Reply, error) {
	if in.MessageID == "" {
		return adapters.Reply{}, adapters.ErrMissingMessageID
	}
	a.mu.Lock()
	a.calls[in.MessageID]++
	a.total++
	a.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return adapters.Reply{}, err
	}
	return adapters.Reply{
		InReplyTo:      in.MessageID,
		ConversationID: in.ConversationID,
		Content:        in.Content,
	}, nil
}

// Invocations reports how many times Handle ran for messageID.
func (a *Adapter) Invocations(messageID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls[messageID]
}

// TotalInvocations reports how many times Handle ran across all messages.
func (a *Adapter) TotalInvocations() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}
