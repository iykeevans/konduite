package messages_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/messages"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
)

func validMessage() messages.NewMessage {
	return messages.NewMessage{
		ConversationID: uuid.New(),
		Sender:         messages.Sender{Kind: conversations.KindUser, ID: uuid.New()},
		IdempotencyKey: "req-1",
		Type:           messages.TypeText,
		Content:        "hello",
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		change func(*messages.NewMessage)
		field  string
	}{
		{"valid", func(*messages.NewMessage) {}, ""},
		{"task with metadata", func(n *messages.NewMessage) {
			n.Type = messages.TypeTask
			n.Metadata = json.RawMessage(`{"priority":"high"}`)
		}, ""},
		{"system from a runtime", func(n *messages.NewMessage) {
			n.Type = messages.TypeSystem
			n.Sender.Kind = conversations.KindRuntime
		}, ""},
		{"missing conversation", func(n *messages.NewMessage) { n.ConversationID = uuid.Nil }, "conversation_id"},
		{"unknown sender kind", func(n *messages.NewMessage) { n.Sender.Kind = "slack" }, "sender.type"},
		{"missing sender id", func(n *messages.NewMessage) { n.Sender.ID = uuid.Nil }, "sender.id"},
		{"missing idempotency key", func(n *messages.NewMessage) { n.IdempotencyKey = "" }, "idempotency_key"},
		{"idempotency key too long", func(n *messages.NewMessage) { n.IdempotencyKey = strings.Repeat("k", 256) }, "idempotency_key"},
		{"unknown type", func(n *messages.NewMessage) { n.Type = "poll" }, "message_type"},
		{"empty content", func(n *messages.NewMessage) { n.Content = "  \n" }, "content"},
		{"content too long", func(n *messages.NewMessage) { n.Content = strings.Repeat("x", messages.MaxContentLen+1) }, "content"},
		{"metadata is an array", func(n *messages.NewMessage) { n.Metadata = json.RawMessage(`[1]`) }, "metadata"},
		{"metadata is not json", func(n *messages.NewMessage) { n.Metadata = json.RawMessage(`{`) }, "metadata"},
		{"parent is nil uuid", func(n *messages.NewMessage) { p := uuid.Nil; n.ParentID = &p }, "parent_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := validMessage()
			tc.change(&n)
			err := n.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.KindInvalid {
				t.Fatalf("err = %v, want errs.Invalid", err)
			}
			if _, ok := e.Fields[tc.field]; !ok {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tc.field)
			}
		})
	}
}

func TestValidateAcceptsMultibyteContentUpToTheLimit(t *testing.T) {
	n := validMessage()
	n.Content = strings.Repeat("é", messages.MaxContentLen)
	if err := n.Validate(); err != nil {
		t.Errorf("err = %v, want nil: the limit counts characters, not bytes", err)
	}
}

type outbox struct {
	rows []store.InsertOutboxEventParams
}

func (o *outbox) InsertOutboxEvent(_ context.Context, arg store.InsertOutboxEventParams) (store.OutboxEvent, error) {
	o.rows = append(o.rows, arg)
	return store.OutboxEvent{}, nil
}

func TestCreatedEventPassesTheContractAndCarriesNoContent(t *testing.T) {
	parent := uuid.New()
	m := messages.Message{
		ID:             uuid.New(),
		ConversationID: uuid.New(),
		SequenceNumber: 7,
		Sender:         messages.Sender{Kind: conversations.KindAgent, ID: uuid.New()},
		ParentID:       &parent,
		Type:           messages.TypeTask,
		Content:        "deploy with token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
	}
	workspace, project := uuid.New(), uuid.New()
	var o outbox
	if _, err := events.Enqueue(t.Context(), &o, messages.CreatedEvent(m, workspace, &project)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if len(o.rows) != 1 || o.rows[0].EventType != "message.created" || o.rows[0].AggregateID != m.ID {
		t.Fatalf("outbox rows = %+v", o.rows)
	}
	env, err := validate.Event(o.rows[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if env.WorkspaceID != workspace.String() || env.ProjectID == nil || *env.ProjectID != project.String() {
		t.Errorf("envelope tenant = %s/%v", env.WorkspaceID, env.ProjectID)
	}
	var data contracts.MessageCreated
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.MessageID != m.ID.String() || data.ConversationID != m.ConversationID.String() || data.SequenceNumber != 7 ||
		data.Sender.Type != "agent" || data.Sender.ID != m.Sender.ID.String() || data.MessageType != "task" ||
		data.ParentID == nil || *data.ParentID != parent.String() {
		t.Errorf("data = %+v", data)
	}
	if strings.Contains(string(env.Data), "deploy") {
		t.Errorf("event data carries message content: %s", env.Data)
	}
}
