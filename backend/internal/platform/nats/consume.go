package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/iykeevans/konduite/backend/internal/platform/log"
)

// Headers on a dead-lettered message. HeaderEventID is also set on a replay,
// whose Nats-Msg-Id is new, so consumers still see the original event id.
const (
	HeaderEventID   = "Konduite-Event-Id"
	HeaderSubject   = "Konduite-Dlq-Subject"
	HeaderConsumer  = "Konduite-Dlq-Consumer"
	HeaderStreamSeq = "Konduite-Dlq-Stream-Seq"
	HeaderAttempts  = "Konduite-Dlq-Attempts"
	HeaderError     = "Konduite-Dlq-Error"
	HeaderFailedAt  = "Konduite-Dlq-Failed-At"
)

const (
	dlqPrefix = "dlq."
	// The server's MaxDeliver sits this far past the runner's last attempt,
	// so a crash or ack timeout on that attempt is redelivered once more
	// and dead-lettered instead of silently dropped by the server.
	serverDeliverHeadroom = 3
	maxErrorLen           = 512
	settleTimeout         = 5 * time.Second
)

// Delivery is one event handed to a Handler. Attempt starts at 1.
type Delivery struct {
	Subject string
	EventID string
	Data    []byte
	Attempt int
}

// Handler processes one delivery. Returning nil acks it. Any other error
// retries it after a backoff, unless it is wrapped with Permanent or the
// attempts are used up, in which case the event goes to the DLQ.
// Handlers must be idempotent: a delivery can arrive more than once.
type Handler func(ctx context.Context, d Delivery) error

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as one a retry cannot fix, such as a payload that
// fails validation, so the event is dead-lettered on this attempt.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// ConsumerSpec configures a durable consumer run by Client.Consume.
type ConsumerSpec struct {
	// Durable names the consumer. It is also recorded on dead letters.
	Durable string
	// FilterSubjects are the agenthub.> subjects to receive.
	FilterSubjects []string
	// MaxDeliver is how many times the handler sees an event before it is
	// dead-lettered. Default 5.
	MaxDeliver int
	// Backoff is the wait before attempt n+1, indexed by n-1; the last step
	// repeats. Default 1s, 5s, 30s, 2m.
	Backoff []time.Duration
	// AckWait bounds one handler call; an unacked event is redelivered
	// after it. Default 30s.
	AckWait time.Duration
	Logger  *slog.Logger
}

func (s ConsumerSpec) withDefaults() (ConsumerSpec, error) {
	if s.Durable == "" {
		return s, errors.New("nats: consumer needs a durable name")
	}
	if len(s.FilterSubjects) == 0 {
		return s, errors.New("nats: consumer needs at least one filter subject")
	}
	for _, f := range s.FilterSubjects {
		if !strings.HasPrefix(f, "agenthub.") {
			return s, fmt.Errorf("nats: filter subject %q is outside agenthub.>", f)
		}
	}
	for _, d := range s.Backoff {
		if d <= 0 {
			return s, errors.New("nats: backoff steps must be positive")
		}
	}
	if s.MaxDeliver <= 0 {
		s.MaxDeliver = 5
	}
	if len(s.Backoff) == 0 {
		s.Backoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute}
	}
	if s.AckWait <= 0 {
		s.AckWait = 30 * time.Second
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	return s, nil
}

func (s ConsumerSpec) consumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:        s.Durable,
		FilterSubjects: s.FilterSubjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        s.AckWait,
		MaxDeliver:     s.MaxDeliver + serverDeliverHeadroom,
	}
}

type outcome int

const (
	ack outcome = iota
	retry
	deadLetter
)

func (o outcome) String() string {
	return [...]string{"ack", "retry", "dead-letter"}[o]
}

func decide(err error, attempt, maxDeliver int, backoff []time.Duration) (outcome, time.Duration) {
	switch {
	case err == nil:
		return ack, 0
	case IsPermanent(err), attempt >= maxDeliver:
		return deadLetter, 0
	}
	step := min(attempt-1, len(backoff)-1)
	return retry, backoff[max(step, 0)]
}

func attemptNumber(numDelivered uint64) int {
	if numDelivered > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(numDelivered)
}

// Consume runs h for every event on spec's durable consumer until ctx is
// cancelled, then returns nil. A handler call already running is allowed
// to finish. It returns an error only when the consumer can't be set up or
// the subscription fails, so the worker restarts.
//
// Dead-lettering publishes to dlq.<subject> before terminating the
// original, so a crash in between leaves the event redelivered rather
// than lost; the dead letter's message id makes the second publish a
// no-op.
func (c *Client) Consume(ctx context.Context, spec ConsumerSpec, h Handler) error {
	s, err := spec.withDefaults()
	if err != nil {
		return err
	}
	cons, err := c.js.CreateOrUpdateConsumer(ctx, Stream, s.consumerConfig())
	if err != nil {
		return fmt.Errorf("consumer %s: %w", s.Durable, err)
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return fmt.Errorf("consumer %s: %w", s.Durable, err)
	}
	defer it.Stop()

	for {
		msg, err := it.Next(jetstream.NextContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, jetstream.ErrNoHeartbeat) {
				s.Logger.Warn("consumer missed a heartbeat", "consumer", s.Durable)
				continue
			}
			return fmt.Errorf("consumer %s: %w", s.Durable, err)
		}
		c.handle(ctx, s, h, msg)
	}
}

func (c *Client) handle(ctx context.Context, s ConsumerSpec, h Handler, msg jetstream.Msg) {
	meta, err := msg.Metadata()
	if err != nil {
		s.Logger.Error("consumer got a message without metadata", "consumer", s.Durable, "error", err)
		_ = msg.Term()
		return
	}
	d := Delivery{Subject: msg.Subject(), EventID: eventID(msg.Headers()), Data: msg.Data(), Attempt: attemptNumber(meta.NumDelivered)}
	logger := s.Logger.With("consumer", s.Durable, "subject", d.Subject, "event_id", d.EventID, "attempt", d.Attempt)

	var (
		herr  error
		out   outcome
		delay time.Duration
	)
	if d.Attempt > s.MaxDeliver {
		herr, out = errors.New("no result from the final attempt: the handler crashed or overran the ack wait"), deadLetter
	} else {
		herr = call(context.WithoutCancel(ctx), s.AckWait, h, d)
		out, delay = decide(herr, d.Attempt, s.MaxDeliver, s.Backoff)
	}

	switch out {
	case ack:
		if err := msg.Ack(); err != nil {
			logger.Warn("ack failed; the event will be redelivered", "error", err)
		}
	case retry:
		logger.Warn("handler failed; retrying", "error", herr, "retry_in", delay)
		if err := msg.NakWithDelay(delay); err != nil {
			logger.Warn("nak failed; the event will be redelivered after the ack wait", "error", err)
		}
	case deadLetter:
		c.deadLetter(ctx, s, msg, meta, d, herr, logger)
	}
}

func (c *Client) deadLetter(ctx context.Context, s ConsumerSpec, msg jetstream.Msg, meta *jetstream.MsgMetadata, d Delivery, herr error, logger *slog.Logger) {
	m := deadLetterMessage(deadLetterInput{
		Subject: d.Subject, EventID: d.EventID, Consumer: s.Durable, StreamSeq: meta.Sequence.Stream,
		Attempts: d.Attempt, Err: herr, FailedAt: time.Now().UTC(), Data: d.Data, Header: msg.Headers(),
	})
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if _, err := c.js.PublishMsg(pubCtx, m); err != nil {
		logger.Error("dead-letter publish failed; retrying the event", "error", err)
		_ = msg.NakWithDelay(s.Backoff[len(s.Backoff)-1])
		return
	}
	logger.Error("event dead-lettered", "error", herr, "dlq_subject", m.Subject)
	if err := msg.TermWithReason("dead-lettered"); err != nil {
		logger.Warn("term failed; the redelivery will be dead-lettered again and deduped", "error", err)
	}
}

func call(ctx context.Context, timeout time.Duration, h Handler, d Delivery) (err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panic: %v", p)
		}
	}()
	return h(ctx, d)
}

func eventID(h natsgo.Header) string {
	if id := h.Get(HeaderEventID); id != "" {
		return id
	}
	return h.Get(jetstream.MsgIDHeader)
}

type deadLetterInput struct {
	Subject   string
	EventID   string
	Consumer  string
	StreamSeq uint64
	Attempts  int
	Err       error
	FailedAt  time.Time
	Data      []byte
	Header    natsgo.Header
}

func deadLetterMessage(in deadLetterInput) *natsgo.Msg {
	m := natsgo.NewMsg(dlqPrefix + in.Subject)
	m.Data = in.Data
	for k, vs := range in.Header {
		if strings.HasPrefix(k, "Nats-") {
			continue
		}
		for _, v := range vs {
			m.Header.Add(k, v)
		}
	}
	errText := "unknown error"
	if in.Err != nil {
		errText = log.Scrub(in.Err.Error())
	}
	if len(errText) > maxErrorLen {
		errText = strings.ToValidUTF8(errText[:maxErrorLen], "")
	}
	m.Header.Set(jetstream.MsgIDHeader, "dlq:"+in.Consumer+":"+strconv.FormatUint(in.StreamSeq, 10))
	m.Header.Set(HeaderEventID, in.EventID)
	m.Header.Set(HeaderSubject, in.Subject)
	m.Header.Set(HeaderConsumer, in.Consumer)
	m.Header.Set(HeaderStreamSeq, strconv.FormatUint(in.StreamSeq, 10))
	m.Header.Set(HeaderAttempts, strconv.Itoa(in.Attempts))
	m.Header.Set(HeaderError, errText)
	m.Header.Set(HeaderFailedAt, in.FailedAt.UTC().Format(time.RFC3339Nano))
	return m
}
