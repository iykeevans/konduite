package nats

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

var (
	// ErrDeadLetterNotFound means no dead letter has that sequence.
	ErrDeadLetterNotFound = errors.New("nats: dead letter not found")
	// ErrNotReplayable means the entry lacks a valid original subject.
	ErrNotReplayable = errors.New("nats: dead letter has no original agenthub subject")
)

// DeadLetter describes one AGENTHUB_DLQ entry. It leaves out the payload,
// which can carry any workspace's data.
type DeadLetter struct {
	Seq      uint64
	Subject  string
	EventID  string
	Consumer string
	Attempts int
	Error    string
	FailedAt time.Time
	StoredAt time.Time
	Size     int
}

// DeadLetters returns up to limit entries with a sequence above after,
// oldest first.
func (c *Client) DeadLetters(ctx context.Context, after uint64, limit int) ([]DeadLetter, error) {
	stream, err := c.js.Stream(ctx, DLQStream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dlq stream: %w", err)
	}
	var out []DeadLetter
	for seq := after + 1; len(out) < limit; {
		raw, err := stream.GetMsg(ctx, seq, jetstream.WithGetMsgSubject(dlqPrefix+">"))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("dlq read %d: %w", seq, err)
		}
		out = append(out, deadLetterFrom(raw))
		seq = raw.Sequence + 1
	}
	return out, nil
}

// ReplayDeadLetter republishes entry seq to its original subject and then
// removes it from the DLQ. Every consumer on that subject receives it
// again and is expected to dedupe by event id.
//
// The replay's Nats-Msg-Id is replay:<seq>, not the original id: reusing
// the original inside the stream's dedupe window would make JetStream drop
// the replay silently. A repeated replay of the same entry within the
// window is dropped, which makes a double click harmless. The original id
// travels in the Konduite-Event-Id header.
func (c *Client) ReplayDeadLetter(ctx context.Context, seq uint64) error {
	stream, err := c.js.Stream(ctx, DLQStream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return ErrDeadLetterNotFound
	}
	if err != nil {
		return fmt.Errorf("dlq stream: %w", err)
	}
	raw, err := stream.GetMsg(ctx, seq)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return ErrDeadLetterNotFound
	}
	if err != nil {
		return fmt.Errorf("dlq read %d: %w", seq, err)
	}
	m, err := replayMessage(raw)
	if err != nil {
		return err
	}
	if _, err := c.js.PublishMsg(ctx, m); err != nil {
		return fmt.Errorf("replay %d: %w", seq, err)
	}
	if err := stream.DeleteMsg(ctx, seq); err != nil && !errors.Is(err, jetstream.ErrMsgNotFound) {
		return fmt.Errorf("replayed %d but could not remove it from the DLQ: %w", seq, err)
	}
	return nil
}

func replayMessage(raw *jetstream.RawStreamMsg) (*natsgo.Msg, error) {
	subject := raw.Header.Get(HeaderSubject)
	if !strings.HasPrefix(subject, "agenthub.") || raw.Subject != dlqPrefix+subject {
		return nil, ErrNotReplayable
	}
	m := natsgo.NewMsg(subject)
	m.Data = raw.Data
	for k, vs := range raw.Header {
		if strings.HasPrefix(k, "Nats-") || strings.HasPrefix(k, "Konduite-Dlq-") {
			continue
		}
		for _, v := range vs {
			m.Header.Add(k, v)
		}
	}
	m.Header.Set(jetstream.MsgIDHeader, "replay:"+strconv.FormatUint(raw.Sequence, 10))
	return m, nil
}

func deadLetterFrom(raw *jetstream.RawStreamMsg) DeadLetter {
	h := raw.Header
	attempts, _ := strconv.Atoi(h.Get(HeaderAttempts))
	failedAt, _ := time.Parse(time.RFC3339Nano, h.Get(HeaderFailedAt))
	return DeadLetter{
		Seq:      raw.Sequence,
		Subject:  h.Get(HeaderSubject),
		EventID:  h.Get(HeaderEventID),
		Consumer: h.Get(HeaderConsumer),
		Attempts: attempts,
		Error:    h.Get(HeaderError),
		FailedAt: failedAt,
		StoredAt: raw.Time,
		Size:     len(raw.Data),
	}
}
