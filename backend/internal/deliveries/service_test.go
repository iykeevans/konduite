package deliveries

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/store"
)

func ptr(id uuid.UUID) *uuid.UUID { return &id }

func TestRecipients(t *testing.T) {
	alice, bob := uuid.New(), uuid.New()
	agentA, agentB, orphanAgent := uuid.New(), uuid.New(), uuid.New()
	rt1, rt2, unknownRT := uuid.New(), uuid.New(), uuid.New()

	user := func(id uuid.UUID) Participant { return Participant{Kind: conversations.KindUser, ID: id} }
	agent := func(id uuid.UUID, rt *uuid.UUID) Participant {
		return Participant{Kind: conversations.KindAgent, ID: id, RuntimeID: rt}
	}
	runtime := func(id uuid.UUID, known bool) Participant {
		p := Participant{Kind: conversations.KindRuntime, ID: id}
		if known {
			p.RuntimeID = ptr(id)
		}
		return p
	}

	tests := []struct {
		name         string
		sender       Sender
		participants []Participant
		want         []Recipient
	}{
		{
			name:         "sender is excluded",
			sender:       Sender{Kind: conversations.KindUser, ID: alice},
			participants: []Participant{user(alice), user(bob)},
			want:         []Recipient{{Kind: conversations.KindUser, ID: bob}},
		},
		{
			name:         "agent routes to its runtime",
			sender:       Sender{Kind: conversations.KindUser, ID: alice},
			participants: []Participant{user(alice), agent(agentA, ptr(rt1))},
			want:         []Recipient{{Kind: conversations.KindAgent, ID: agentA, RuntimeID: ptr(rt1)}},
		},
		{
			name:         "runtime routes to itself",
			sender:       Sender{Kind: conversations.KindUser, ID: alice},
			participants: []Participant{runtime(rt2, true)},
			want:         []Recipient{{Kind: conversations.KindRuntime, ID: rt2, RuntimeID: ptr(rt2)}},
		},
		{
			name:         "agent and runtime without a known runtime are skipped",
			sender:       Sender{Kind: conversations.KindUser, ID: alice},
			participants: []Participant{agent(orphanAgent, nil), runtime(unknownRT, false), user(bob)},
			want:         []Recipient{{Kind: conversations.KindUser, ID: bob}},
		},
		{
			name:         "sending agent is excluded but its runtime's other agent is not",
			sender:       Sender{Kind: conversations.KindAgent, ID: agentA},
			participants: []Participant{agent(agentA, ptr(rt1)), agent(agentB, ptr(rt1)), user(alice)},
			want: []Recipient{
				{Kind: conversations.KindAgent, ID: agentB, RuntimeID: ptr(rt1)},
				{Kind: conversations.KindUser, ID: alice},
			},
		},
		{
			name:         "same id with another kind is not the sender",
			sender:       Sender{Kind: conversations.KindRuntime, ID: rt1},
			participants: []Participant{runtime(rt1, true), agent(agentA, ptr(rt1))},
			want:         []Recipient{{Kind: conversations.KindAgent, ID: agentA, RuntimeID: ptr(rt1)}},
		},
		{
			name:         "only the sender",
			sender:       Sender{Kind: conversations.KindUser, ID: alice},
			participants: []Participant{user(alice)},
			want:         nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Recipients(tc.sender, tc.participants)
			if !slices.EqualFunc(got, tc.want, equalRecipient) {
				t.Errorf("Recipients = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func equalRecipient(a, b Recipient) bool {
	return a.Kind == b.Kind && a.ID == b.ID && equalPtr(a.RuntimeID, b.RuntimeID)
}

func equalPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestWakeUpsGroupsNewDeliveriesByRuntime(t *testing.T) {
	rt1, rt2 := uuid.New(), uuid.New()
	d := func(rt *uuid.UUID) store.MessageDelivery {
		return store.MessageDelivery{ID: uuid.New(), RuntimeID: rt, CreatedAt: time.Now()}
	}
	rows := []store.MessageDelivery{d(ptr(rt1)), d(nil), d(ptr(rt2)), d(ptr(rt1))}

	got := wakeUps(rows)

	if len(got) != 2 {
		t.Fatalf("wakeUps = %d runtimes, want 2: %+v", len(got), got)
	}
	if got[0].runtime != rt1 || !slices.Equal(got[0].deliveries, []uuid.UUID{rows[0].ID, rows[3].ID}) {
		t.Errorf("first = %+v, want rt1 with rows 0 and 3", got[0])
	}
	if got[1].runtime != rt2 || !slices.Equal(got[1].deliveries, []uuid.UUID{rows[2].ID}) {
		t.Errorf("second = %+v, want rt2 with row 2", got[1])
	}
	if len(wakeUps([]store.MessageDelivery{d(nil)})) != 0 {
		t.Error("a user-only delivery should wake no runtime")
	}
}

func TestNotifySubject(t *testing.T) {
	id := uuid.MustParse("5b0c7b1e-8f4c-4c1a-9d43-0e6f3b6f2a11")
	if got, want := NotifySubject(id), "agenthub.rt.5b0c7b1e-8f4c-4c1a-9d43-0e6f3b6f2a11.deliver"; got != want {
		t.Errorf("NotifySubject = %q, want %q", got, want)
	}
}

func TestHandleRejectsBadEventsAsPermanent(t *testing.T) {
	envelope := func(typ string, version int, data any) []byte {
		b, _ := json.Marshal(map[string]any{
			"id": uuid.New(), "type": typ, "version": version, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
			"workspace_id": uuid.New(), "correlation_id": uuid.New(), "data": data,
		})
		return b
	}
	tests := map[string][]byte{
		"not json":       []byte("{"),
		"wrong type":     envelope("runtime.connected", 1, map[string]any{"runtime_id": uuid.New()}),
		"broken payload": envelope("message.created", 1, map[string]any{"message_id": "nope"}),
	}
	r := NewRouter(nil, nil)
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			err := r.Handle(t.Context(), nats.Delivery{Subject: "agenthub.message.created", Data: data, Attempt: 1})
			if !nats.IsPermanent(err) {
				t.Errorf("Handle = %v, want a permanent error", err)
			}
		})
	}
}

func TestConsumerSpecFiltersMessageCreated(t *testing.T) {
	s := ConsumerSpec(nil)
	if s.Durable == "" || !slices.Equal(s.FilterSubjects, []string{"agenthub.message.created"}) {
		t.Errorf("spec = %+v", s)
	}
}
