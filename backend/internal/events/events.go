// Package events builds event envelopes and writes them to the transactional
// outbox. The relay publishes each outbox row unchanged to Subject(type), or
// to the event's Subject override, with Nats-Msg-Id set to the row id, which
// is also the envelope id.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/platform/log"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/packages/contracts"
	"github.com/iykeevans/konduite/packages/contracts/validate"
)

var (
	// ErrInvalid means the event failed the envelope or payload schema.
	ErrInvalid = errors.New("invalid event")
	// ErrCredential means the payload carries a credential-like key or value.
	ErrCredential = errors.New("event payload carries a credential")
)

// Event is what a domain write announces. The envelope id, occurrence time
// and correlation id are filled in by Enqueue.
type Event struct {
	Type        string
	Version     int
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	AggregateID uuid.UUID
	CausationID *uuid.UUID
	Data        any
	// Subject, when set, replaces Subject(Type) as the NATS subject, for
	// events addressed to one recipient such as agenthub.rt.<id>.deliver.
	// It must be a literal subject under agenthub.
	Subject string
}

// Outbox inserts outbox rows. *store.Queries satisfies it.
type Outbox interface {
	InsertOutboxEvent(ctx context.Context, arg store.InsertOutboxEventParams) (store.OutboxEvent, error)
}

// Subject is the NATS subject an event type is published on.
func Subject(eventType string) string {
	return "agenthub." + eventType
}

// Enqueue validates ev and inserts it into the outbox, returning the event id.
//
// q must be bound to the transaction that makes the change the event
// announces (store.New(tx) inside db.WithTx), so the event commits or rolls
// back with it. Passing a pool-bound q breaks the outbox guarantee.
//
// The correlation id comes from ctx (log.CorrelationID). A non-UUID id maps
// to a stable UUID; with none, the event starts its own chain.
func Enqueue(ctx context.Context, q Outbox, ev Event) (uuid.UUID, error) {
	if ev.WorkspaceID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: workspace id is required", ErrInvalid)
	}
	if ev.AggregateID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: aggregate id is required", ErrInvalid)
	}
	var subject *string
	if ev.Subject != "" {
		if !literalSubject(ev.Subject) {
			return uuid.Nil, fmt.Errorf("%w: subject %q is not a literal subject under agenthub.", ErrInvalid, ev.Subject)
		}
		subject = &ev.Subject
	}
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: marshal %s data: %w", ErrInvalid, ev.Type, err)
	}
	if err := checkCredentials(data); err != nil {
		return uuid.Nil, fmt.Errorf("%s: %w", ev.Type, err)
	}

	id := uuid.New()
	env := contracts.Envelope{
		ID:            id.String(),
		Type:          ev.Type,
		Version:       ev.Version,
		OccurredAt:    time.Now().UTC(),
		WorkspaceID:   ev.WorkspaceID.String(),
		ProjectID:     optional(ev.ProjectID),
		CorrelationID: correlationID(ctx, id).String(),
		CausationID:   optional(ev.CausationID),
		Data:          data,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: marshal envelope: %w", ErrInvalid, err)
	}
	if _, err := validate.Event(payload); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	workspace := ev.WorkspaceID
	if _, err := q.InsertOutboxEvent(ctx, store.InsertOutboxEventParams{
		ID:          id,
		EventType:   ev.Type,
		AggregateID: ev.AggregateID,
		WorkspaceID: &workspace,
		Payload:     payload,
		Subject:     subject,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("insert %s outbox event: %w", ev.Type, err)
	}
	return id, nil
}

func literalSubject(s string) bool {
	rest, ok := strings.CutPrefix(s, "agenthub.")
	if !ok {
		return false
	}
	for _, tok := range strings.Split(rest, ".") {
		if tok == "" || tok == "*" || tok == ">" || strings.ContainsAny(tok, " \t\r\n") {
			return false
		}
	}
	return true
}

func correlationID(ctx context.Context, eventID uuid.UUID) uuid.UUID {
	raw := log.CorrelationID(ctx)
	if raw == "" {
		return eventID
	}
	if id, err := uuid.Parse(raw); err == nil {
		return id
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("konduite:correlation:"+raw))
}

func optional(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// The error names the offending path, never the value.
func checkCredentials(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("%w: decode data: %w", ErrInvalid, err)
	}
	return walk("data", v)
}

func walk(path string, v any) error {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			p := path + "." + k
			if log.SensitiveKey(k) {
				return fmt.Errorf("%w: key %s", ErrCredential, p)
			}
			if err := walk(p, child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range v {
			if err := walk(path+"["+strconv.Itoa(i)+"]", child); err != nil {
				return err
			}
		}
	case string:
		if log.SecretValue(v) {
			return fmt.Errorf("%w: value at %s", ErrCredential, path)
		}
	}
	return nil
}
