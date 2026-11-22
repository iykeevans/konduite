package deliveries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
)

const (
	messageCreated = "message.created"
	runtimeDeliver = "runtime.deliver"
	durable        = "message-router"
)

// NotifySubject is where a runtime's connection hears that it has pending
// deliveries.
func NotifySubject(runtimeID uuid.UUID) string {
	return "agenthub.rt." + runtimeID.String() + ".deliver"
}

// ConsumerSpec is the durable consumer the router runs on. A nil logger
// uses slog.Default().
func ConsumerSpec(logger *slog.Logger) nats.ConsumerSpec {
	return nats.ConsumerSpec{
		Durable:        durable,
		FilterSubjects: []string{events.Subject(messageCreated)},
		Logger:         logger,
	}
}

// Recipients is everyone in the conversation except the sender, in
// participant order. Agents and runtimes with no runtime in the workspace
// are dropped: there is nothing to wake and the delivery could never be
// pushed.
func Recipients(sender Sender, participants []Participant) []Recipient {
	var out []Recipient
	for _, p := range participants {
		if p.Kind == sender.Kind && p.ID == sender.ID {
			continue
		}
		if p.Kind != conversations.KindUser && p.RuntimeID == nil {
			continue
		}
		r := Recipient{Kind: p.Kind, ID: p.ID}
		if p.Kind != conversations.KindUser {
			r.RuntimeID = p.RuntimeID
		}
		out = append(out, r)
	}
	return out
}

// Router routes message.created events to message_deliveries.
type Router struct {
	pool   db.Beginner
	logger *slog.Logger
}

// NewRouter returns a router writing through pool. A nil logger uses
// slog.Default().
func NewRouter(pool db.Beginner, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{pool: pool, logger: logger}
}

// Handle routes one message.created event. It is a nats.Handler.
//
// The deliveries and their runtime.deliver events commit in one
// transaction. A redelivered event inserts no rows, because of
// UNIQUE (message_id, recipient_id), and so enqueues no events: the unique
// key is the dedupe, not the event id. A message that no longer exists,
// or an event that fails its schema, is permanent and goes to the DLQ.
func (r *Router) Handle(ctx context.Context, d nats.Delivery) error {
	env, ev, err := decode(d.Data)
	if err != nil {
		return nats.Permanent(err)
	}
	messageID, err := uuid.Parse(ev.MessageID)
	if err != nil {
		return nats.Permanent(fmt.Errorf("message id: %w", err))
	}
	cause, err := uuid.Parse(env.ID)
	if err != nil {
		return nats.Permanent(fmt.Errorf("event id: %w", err))
	}
	ctx = log.WithCorrelationID(ctx, env.CorrelationID)

	var created []store.MessageDelivery
	err = db.WithTx(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		created, err = route(ctx, store.New(tx), messageID, cause)
		return err
	})
	if err != nil {
		return err
	}
	r.logger.InfoContext(ctx, "message routed", "message_id", messageID, "event_id", d.EventID,
		"new_deliveries", len(created), "runtimes_woken", len(wakeUps(created)))
	return nil
}

func decode(raw []byte) (contracts.Envelope, contracts.MessageCreated, error) {
	var ev contracts.MessageCreated
	env, err := validate.Event(raw)
	if err != nil {
		return env, ev, err
	}
	if env.Type != messageCreated || env.Version != 1 {
		return env, ev, fmt.Errorf("unexpected event %s v%d", env.Type, env.Version)
	}
	if err := json.Unmarshal(env.Data, &ev); err != nil {
		return env, ev, fmt.Errorf("decode %s: %w", messageCreated, err)
	}
	return env, ev, nil
}

func route(ctx context.Context, q *store.Queries, messageID, cause uuid.UUID) ([]store.MessageDelivery, error) {
	m, err := q.GetMessageForRouting(ctx, messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nats.Permanent(fmt.Errorf("message %s not found", messageID))
	}
	if err != nil {
		return nil, fmt.Errorf("load message: %w", err)
	}
	rows, err := q.ListRoutingParticipants(ctx, store.ListRoutingParticipantsParams{
		WorkspaceID: m.WorkspaceID, ConversationID: m.ConversationID,
	})
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	participants := make([]Participant, 0, len(rows))
	for _, row := range rows {
		p := Participant{Kind: conversations.ParticipantKind(row.ParticipantType), ID: row.ParticipantID}
		switch p.Kind {
		case conversations.KindAgent:
			p.RuntimeID = row.AgentRuntimeID
		case conversations.KindRuntime:
			p.RuntimeID = row.KnownRuntimeID
		}
		participants = append(participants, p)
	}
	sender := Sender{Kind: conversations.ParticipantKind(m.SenderType), ID: m.SenderID}

	var created []store.MessageDelivery
	for _, rc := range Recipients(sender, participants) {
		row, err := q.InsertDelivery(ctx, store.InsertDeliveryParams{
			WorkspaceID:    m.WorkspaceID,
			MessageID:      m.ID,
			ConversationID: m.ConversationID,
			SequenceNumber: m.SequenceNumber,
			RecipientType:  string(rc.Kind),
			RecipientID:    rc.ID,
			RuntimeID:      rc.RuntimeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("insert delivery: %w", err)
		}
		created = append(created, row)
	}

	for _, w := range wakeUps(created) {
		ids := make([]string, len(w.deliveries))
		for i, id := range w.deliveries {
			ids[i] = id.String()
		}
		if _, err := events.Enqueue(ctx, q, events.Event{
			Type:        runtimeDeliver,
			Version:     1,
			WorkspaceID: m.WorkspaceID,
			ProjectID:   m.ProjectID,
			AggregateID: m.ID,
			CausationID: &cause,
			Subject:     NotifySubject(w.runtime),
			Data: contracts.RuntimeDeliver{
				RuntimeID:      w.runtime.String(),
				MessageID:      m.ID.String(),
				ConversationID: m.ConversationID.String(),
				SequenceNumber: int(m.SequenceNumber),
				DeliveryIds:    ids,
			},
		}); err != nil {
			return nil, err
		}
	}
	return created, nil
}

type wakeUp struct {
	runtime    uuid.UUID
	deliveries []uuid.UUID
}

func wakeUps(created []store.MessageDelivery) []wakeUp {
	var out []wakeUp
	index := map[uuid.UUID]int{}
	for _, d := range created {
		if d.RuntimeID == nil {
			continue
		}
		i, ok := index[*d.RuntimeID]
		if !ok {
			i = len(out)
			index[*d.RuntimeID] = i
			out = append(out, wakeUp{runtime: *d.RuntimeID})
		}
		out[i].deliveries = append(out[i].deliveries, d.ID)
	}
	return out
}
