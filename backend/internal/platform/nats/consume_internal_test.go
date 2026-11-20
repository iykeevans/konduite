package nats

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestDecide(t *testing.T) {
	backoff := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	boom := errors.New("boom")
	tests := []struct {
		name      string
		err       error
		attempt   int
		want      outcome
		wantDelay time.Duration
	}{
		{"success acks", nil, 1, ack, 0},
		{"success on the last attempt acks", nil, 4, ack, 0},
		{"first failure retries after the first backoff", boom, 1, retry, time.Second},
		{"second failure retries after the second backoff", boom, 2, retry, 5 * time.Second},
		{"failures past the schedule reuse its last step", boom, 3, retry, 30 * time.Second},
		{"failure on the last attempt dead-letters", boom, 4, deadLetter, 0},
		{"permanent error dead-letters on the first attempt", Permanent(boom), 1, deadLetter, 0},
		{"wrapped permanent error dead-letters", fmt.Errorf("handle: %w", Permanent(boom)), 2, deadLetter, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, delay := decide(tt.err, tt.attempt, 4, backoff)
			if got != tt.want || delay != tt.wantDelay {
				t.Errorf("decide(%v, attempt %d) = %v, %v; want %v, %v", tt.err, tt.attempt, got, delay, tt.want, tt.wantDelay)
			}
		})
	}
}

func TestPermanentKeepsTheCauseAndNilStaysNil(t *testing.T) {
	cause := errors.New("bad payload")
	err := Permanent(cause)
	if !errors.Is(err, cause) || !IsPermanent(err) {
		t.Errorf("Permanent(cause): Is cause %v, IsPermanent %v; want both true", errors.Is(err, cause), IsPermanent(err))
	}
	if IsPermanent(cause) {
		t.Error("IsPermanent(plain error) = true")
	}
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) != nil")
	}
}

func TestConsumerSpecDefaultsAndValidation(t *testing.T) {
	s, err := ConsumerSpec{Durable: "d", FilterSubjects: []string{"agenthub.message.>"}}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if s.MaxDeliver != 5 || len(s.Backoff) == 0 || s.AckWait != 30*time.Second || s.Logger == nil {
		t.Errorf("defaults = max %d, backoff %v, ackwait %v, logger %v", s.MaxDeliver, s.Backoff, s.AckWait, s.Logger)
	}

	bad := []ConsumerSpec{
		{FilterSubjects: []string{"agenthub.x"}},
		{Durable: "d"},
		{Durable: "d", FilterSubjects: []string{"dlq.agenthub.x"}},
		{Durable: "d", FilterSubjects: []string{"agenthub.x"}, Backoff: []time.Duration{0}},
	}
	for _, spec := range bad {
		if _, err := spec.withDefaults(); err == nil {
			t.Errorf("withDefaults(%+v): want an error", spec)
		}
	}
}

func TestConsumerConfigLeavesHeadroomPastTheLastAttempt(t *testing.T) {
	s, _ := ConsumerSpec{Durable: "d", FilterSubjects: []string{"agenthub.a", "agenthub.b"}, MaxDeliver: 3}.withDefaults()
	cfg := s.consumerConfig()
	if cfg.Durable != "d" || cfg.AckPolicy != jetstream.AckExplicitPolicy || len(cfg.FilterSubjects) != 2 {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.MaxDeliver <= 3 {
		t.Errorf("server MaxDeliver = %d, want more than the 3 attempts so a crash on the last one still reaches the DLQ", cfg.MaxDeliver)
	}
}

func TestDeadLetterMessage(t *testing.T) {
	failed := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	m := deadLetterMessage(deadLetterInput{
		Subject:   "agenthub.message.created",
		EventID:   "evt-1",
		Consumer:  "deliveries",
		StreamSeq: 42,
		Attempts:  5,
		Err:       errors.New("push failed: Authorization: Bearer abc.def.ghi " + strings.Repeat("x", 2000)),
		FailedAt:  failed,
		Data:      []byte(`{"id":"evt-1"}`),
	})

	if m.Subject != "dlq.agenthub.message.created" || string(m.Data) != `{"id":"evt-1"}` {
		t.Errorf("subject %q data %q", m.Subject, m.Data)
	}
	h := m.Header
	want := map[string]string{
		jetstream.MsgIDHeader: "dlq:deliveries:42",
		HeaderSubject:         "agenthub.message.created",
		HeaderEventID:         "evt-1",
		HeaderConsumer:        "deliveries",
		HeaderStreamSeq:       "42",
		HeaderAttempts:        "5",
		HeaderFailedAt:        "2026-11-20T10:00:00Z",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	e := h.Get(HeaderError)
	if strings.Contains(e, "abc.def") {
		t.Errorf("error header leaks the bearer token: %q", e)
	}
	if len(e) > maxErrorLen {
		t.Errorf("error header is %d bytes, want at most %d", len(e), maxErrorLen)
	}
}

func TestEventIDPrefersTheReplayHeader(t *testing.T) {
	h := natsgo.Header{}
	h.Set(jetstream.MsgIDHeader, "replay:7")
	h.Set(HeaderEventID, "evt-1")
	if got := eventID(h); got != "evt-1" {
		t.Errorf("eventID = %q, want the original evt-1", got)
	}
	h.Del(HeaderEventID)
	if got := eventID(h); got != "replay:7" {
		t.Errorf("eventID without the header = %q, want the message id", got)
	}
}

func TestDeadLetterFromHeaders(t *testing.T) {
	m := deadLetterMessage(deadLetterInput{
		Subject: "agenthub.a.b", EventID: "evt-9", Consumer: "c", StreamSeq: 3, Attempts: 2,
		Err: errors.New("nope"), FailedAt: time.Unix(100, 0).UTC(), Data: []byte("12345"),
	})
	stored := time.Unix(200, 0).UTC()
	dl := deadLetterFrom(&jetstream.RawStreamMsg{Subject: m.Subject, Sequence: 11, Header: m.Header, Data: m.Data, Time: stored})
	want := DeadLetter{
		Seq: 11, Subject: "agenthub.a.b", EventID: "evt-9", Consumer: "c", Attempts: 2,
		Error: "nope", FailedAt: time.Unix(100, 0).UTC(), StoredAt: stored, Size: 5,
	}
	if dl != want {
		t.Errorf("deadLetterFrom = %+v\nwant %+v", dl, want)
	}
}

func TestReplayMessage(t *testing.T) {
	orig := natsgo.Header{}
	orig.Set("Traceparent", "00-abc-def-01")
	orig.Set(jetstream.MsgIDHeader, "evt-1")
	dl := deadLetterMessage(deadLetterInput{
		Subject: "agenthub.message.created", EventID: "evt-1", Consumer: "c", StreamSeq: 3, Attempts: 5,
		Err: errors.New("x"), FailedAt: time.Now(), Data: []byte("payload"), Header: orig,
	})
	m, err := replayMessage(&jetstream.RawStreamMsg{Subject: dl.Subject, Sequence: 12, Header: dl.Header, Data: dl.Data})
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "agenthub.message.created" || string(m.Data) != "payload" {
		t.Errorf("replay to %q with %q", m.Subject, m.Data)
	}
	if got := m.Header.Get(jetstream.MsgIDHeader); got != "replay:12" {
		t.Errorf("replay msg id = %q, want replay:12", got)
	}
	if m.Header.Get(HeaderEventID) != "evt-1" || m.Header.Get("Traceparent") != "00-abc-def-01" {
		t.Errorf("replay lost the event id or trace headers: %v", m.Header)
	}
	if m.Header.Get(HeaderError) != "" || m.Header.Get(HeaderConsumer) != "" {
		t.Errorf("replay carries DLQ bookkeeping headers: %v", m.Header)
	}

	for _, raw := range []*jetstream.RawStreamMsg{
		{Subject: "dlq.agenthub.x", Header: natsgo.Header{}},
		{Subject: "dlq.agenthub.x", Header: natsgo.Header{HeaderSubject: []string{"agenthub.y"}}},
		{Subject: "dlq.other", Header: natsgo.Header{HeaderSubject: []string{"other"}}},
	} {
		if _, err := replayMessage(raw); !errors.Is(err, ErrNotReplayable) {
			t.Errorf("replayMessage(%s, %v) = %v, want ErrNotReplayable", raw.Subject, raw.Header, err)
		}
	}
}
