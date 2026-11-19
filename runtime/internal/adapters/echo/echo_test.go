package echo_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/iykeevans/konduite/runtime/internal/adapters"
	"github.com/iykeevans/konduite/runtime/internal/adapters/echo"
)

var _ adapters.Adapter = (*echo.Adapter)(nil)

func TestProvider(t *testing.T) {
	if got := echo.New().Provider(); got != "echo" {
		t.Errorf("Provider() = %q, want %q", got, "echo")
	}
}

func TestHandle(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name    string
		ctx     context.Context
		in      adapters.AgentInput
		want    adapters.Reply
		wantErr error
	}{
		{
			name: "replies with the message content",
			ctx:  context.Background(),
			in:   adapters.AgentInput{MessageID: "m1", ConversationID: "c1", Content: "hello"},
			want: adapters.Reply{InReplyTo: "m1", ConversationID: "c1", Content: "hello"},
		},
		{
			name: "replies to an empty message",
			ctx:  context.Background(),
			in:   adapters.AgentInput{MessageID: "m2", ConversationID: "c1"},
			want: adapters.Reply{InReplyTo: "m2", ConversationID: "c1"},
		},
		{
			name:    "rejects a message without an id",
			ctx:     context.Background(),
			in:      adapters.AgentInput{ConversationID: "c1", Content: "hello"},
			wantErr: adapters.ErrMissingMessageID,
		},
		{
			name:    "returns the context error when canceled",
			ctx:     canceled,
			in:      adapters.AgentInput{MessageID: "m3", Content: "hello"},
			wantErr: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := echo.New().Handle(tt.ctx, tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Handle() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Handle() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInvocationCounts(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		calls     []adapters.AgentInput
		ctx       context.Context
		wantByID  map[string]int
		wantTotal int
	}{
		{
			name:      "no calls",
			wantByID:  map[string]int{"m1": 0},
			wantTotal: 0,
		},
		{
			name:      "one call per message",
			calls:     []adapters.AgentInput{{MessageID: "m1"}, {MessageID: "m2"}},
			wantByID:  map[string]int{"m1": 1, "m2": 1, "m3": 0},
			wantTotal: 2,
		},
		{
			name:      "a duplicate delivery counts twice",
			calls:     []adapters.AgentInput{{MessageID: "m1"}, {MessageID: "m1"}, {MessageID: "m2"}},
			wantByID:  map[string]int{"m1": 2, "m2": 1},
			wantTotal: 3,
		},
		{
			name:      "a call without a message id is not counted",
			calls:     []adapters.AgentInput{{}, {MessageID: "m1"}},
			wantByID:  map[string]int{"": 0, "m1": 1},
			wantTotal: 1,
		},
		{
			name:      "a call with a canceled context still counts",
			calls:     []adapters.AgentInput{{MessageID: "m1"}},
			ctx:       canceled,
			wantByID:  map[string]int{"m1": 1},
			wantTotal: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			a := echo.New()
			for _, in := range tt.calls {
				_, _ = a.Handle(ctx, in)
			}
			for id, want := range tt.wantByID {
				if got := a.Invocations(id); got != want {
					t.Errorf("Invocations(%q) = %d, want %d", id, got, want)
				}
			}
			if got := a.TotalInvocations(); got != tt.wantTotal {
				t.Errorf("TotalInvocations() = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}

func TestInvocationCountsAreSafeForConcurrentUse(t *testing.T) {
	const messages, callsPerMessage = 20, 50
	a := echo.New()

	var wg sync.WaitGroup
	for m := range messages {
		id := fmt.Sprintf("m%d", m)
		for range callsPerMessage {
			wg.Go(func() {
				if _, err := a.Handle(context.Background(), adapters.AgentInput{MessageID: id}); err != nil {
					t.Errorf("Handle(%q) error = %v", id, err)
				}
				_ = a.Invocations(id)
			})
		}
	}
	wg.Wait()

	for m := range messages {
		id := fmt.Sprintf("m%d", m)
		if got := a.Invocations(id); got != callsPerMessage {
			t.Errorf("Invocations(%q) = %d, want %d", id, got, callsPerMessage)
		}
	}
	if got, want := a.TotalInvocations(), messages*callsPerMessage; got != want {
		t.Errorf("TotalInvocations() = %d, want %d", got, want)
	}
}
