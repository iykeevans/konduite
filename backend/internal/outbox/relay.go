// Package outbox relays committed outbox_events rows to NATS JetStream
// (ADR-004).
//
// Writers insert the event row in the same transaction as the change it
// announces (db.WithTx). The relay claims unpublished rows with
// FOR UPDATE SKIP LOCKED, publishes each payload unchanged to
// the row's subject, or events.Subject(event_type) when it has none, with
// the row id as Nats-Msg-Id, and sets published_at
// in that same transaction. Several relays can run at once: a row is
// claimed by one of them at a time.
//
// Delivery is at least once. A relay that publishes and then dies before
// committing leaves the row unpublished, and the next pass publishes it
// again under the same id; JetStream drops the repeat if it lands within
// nats.DedupeWindow, and consumers dedupe by event id for anything later.
// Rows are claimed oldest first, but concurrent relays or a retry can
// publish them out of order.
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/events"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/store"
)

// Channel is the Postgres NOTIFY channel the outbox_events insert trigger
// (migration 00005) signals on.
const Channel = "outbox_events"

// Publisher sends one event to the bus. *nats.Client satisfies it.
type Publisher interface {
	Publish(ctx context.Context, subject, msgID string, data []byte) error
}

// Config tunes a Relay. Zero fields take the defaults noted on each.
type Config struct {
	// BatchSize is how many rows one transaction claims. Default 100.
	BatchSize int32
	// PollInterval is the safety pass that runs when no notification
	// arrives, so a missed NOTIFY or a dropped LISTEN connection delays an
	// event by at most this much. Default 5s.
	PollInterval time.Duration
	// MinBackoff and MaxBackoff bound the wait after a failed pass, which
	// doubles per consecutive failure. Defaults 500ms and 30s.
	MinBackoff, MaxBackoff time.Duration
	// PublishTimeout caps a single publish waiting for the stream's ack.
	// Default 5s.
	PublishTimeout time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = 500 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.PublishTimeout <= 0 {
		c.PublishTimeout = 5 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Relay moves outbox rows to the bus.
type Relay struct {
	pool *pgxpool.Pool
	pub  Publisher
	cfg  Config
}

// NewRelay returns a relay reading pool and publishing through pub.
func NewRelay(pool *pgxpool.Pool, pub Publisher, cfg Config) *Relay {
	return &Relay{pool: pool, pub: pub, cfg: cfg.withDefaults()}
}

// Run relays until ctx is cancelled, then returns nil. It is a
// worker.Consumer.
//
// It holds its own connection (outside the pool) to LISTEN on Channel and
// makes a pass whenever a notification arrives or PollInterval passes. A
// failed pass leaves its rows unpublished and waits out a backoff, ignoring
// notifications, before trying again. Losing the LISTEN connection is
// logged and retried; meanwhile the poll keeps events moving.
func (r *Relay) Run(ctx context.Context) error {
	wake := make(chan struct{}, 1)
	ready := make(chan struct{})
	listening := make(chan struct{})
	go func() {
		defer close(listening)
		r.listen(ctx, wake, sync.OnceFunc(func() { close(ready) }))
	}()
	defer func() { <-listening }()

	select {
	case <-ready:
	case <-ctx.Done():
		return nil
	}

	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := r.PublishPending(ctx)
		delay, notified := r.cfg.PollInterval, (<-chan struct{})(wake)
		if err != nil && ctx.Err() == nil {
			failures++
			delay, notified = backoff(r.cfg.MinBackoff, r.cfg.MaxBackoff, failures), nil
			r.cfg.Logger.Warn("outbox relay pass failed; retrying",
				"error", err, "published", n, "retry_in", delay)
		} else {
			failures = 0
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-notified:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// PublishPending publishes every unpublished row, a batch per transaction,
// and returns how many it published. It stops at the first publish
// failure, keeping the batch's earlier rows marked as published. Once ctx
// is cancelled it finishes marking the rows already sent and returns.
func (r *Relay) PublishPending(ctx context.Context) (int, error) {
	total := 0
	for ctx.Err() == nil {
		published, claimed, err := r.publishBatch(ctx)
		total += published
		if err != nil {
			return total, err
		}
		if claimed < int(r.cfg.BatchSize) {
			break
		}
	}
	return total, nil
}

func (r *Relay) publishBatch(ctx context.Context) (published, claimed int, err error) {
	var pubErr error
	// The transaction ignores cancellation so rows already on the bus are
	// marked before shutdown rather than republished on the next start.
	txCtx := context.WithoutCancel(ctx)
	err = db.WithTx(txCtx, r.pool, func(txCtx context.Context, tx pgx.Tx) error {
		q := store.New(tx)
		rows, err := q.ClaimOutboxBatch(txCtx, r.cfg.BatchSize)
		if err != nil {
			return fmt.Errorf("claim outbox batch: %w", err)
		}
		claimed = len(rows)

		sent := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			if ctx.Err() != nil {
				break
			}
			if pubErr = r.publish(ctx, row); pubErr != nil {
				break
			}
			sent = append(sent, row.ID)
		}
		if len(sent) == 0 {
			return nil
		}
		if err := q.MarkOutboxPublished(txCtx, sent); err != nil {
			return fmt.Errorf("mark outbox published: %w", err)
		}
		published = len(sent)
		return nil
	})
	if err != nil {
		return 0, claimed, err
	}
	return published, claimed, pubErr
}

func (r *Relay) publish(ctx context.Context, row store.ClaimOutboxBatchRow) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.PublishTimeout)
	defer cancel()
	subject := events.Subject(row.EventType)
	if row.Subject != nil {
		subject = *row.Subject
	}
	if err := r.pub.Publish(ctx, subject, row.ID.String(), row.Payload); err != nil {
		return fmt.Errorf("publish outbox event %s: %w", row.ID, err)
	}
	return nil
}

// listen keeps a LISTEN connection open until ctx is cancelled, sending on
// wake for every notification. ready is called once the first LISTEN is in
// place, or once the first attempt fails, so Run never waits on it longer
// than one connection attempt.
func (r *Relay) listen(ctx context.Context, wake chan<- struct{}, ready func()) {
	failures := 0
	for {
		listened, err := r.listenOnce(ctx, wake, ready)
		if ctx.Err() != nil {
			return
		}
		ready()
		if listened {
			failures = 0
		}
		failures++
		delay := backoff(r.cfg.MinBackoff, r.cfg.MaxBackoff, failures)
		r.cfg.Logger.Warn("outbox relay lost its LISTEN connection; polling until it reconnects",
			"error", err, "retry_in", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (r *Relay) listenOnce(ctx context.Context, wake chan<- struct{}, ready func()) (listened bool, err error) {
	conn, err := pgx.ConnectConfig(ctx, r.pool.Config().ConnConfig.Copy())
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}
	ready()
	// Rows committed while no LISTEN was in place sent their notification
	// to nobody.
	signal(wake)
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			return true, fmt.Errorf("wait for notification: %w", err)
		}
		signal(wake)
	}
}

func signal(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func backoff(lo, hi time.Duration, failures int) time.Duration {
	d := lo
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= hi {
			return hi
		}
	}
	return min(d, hi)
}
