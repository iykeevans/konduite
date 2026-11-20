//go:build integration

package nats_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/platform/natstest"
)

const waitFor = 20 * time.Second

func fastSpec(durable string) nats.ConsumerSpec {
	return nats.ConsumerSpec{
		Durable:        durable,
		FilterSubjects: []string{"agenthub.message.>"},
		MaxDeliver:     3,
		Backoff:        []time.Duration{10 * time.Millisecond},
		AckWait:        5 * time.Second,
	}
}

func consume(t *testing.T, srv *natstest.Server, spec nats.ConsumerSpec, h nats.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Client.Consume(ctx, spec, h) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Consume returned %v after cancel, want nil", err)
			}
		case <-time.After(waitFor):
			t.Error("Consume did not return after cancel")
		}
	})
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitFor):
		t.Fatal("timed out waiting for a delivery")
		var zero T
		return zero
	}
}

func nextDeadLetter(t *testing.T, srv *natstest.Server) jetstream.Msg {
	t.Helper()
	cons, err := srv.JetStream.OrderedConsumer(t.Context(), nats.DLQStream, jetstream.OrderedConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(waitFor))
	if err != nil {
		t.Fatalf("no dead letter arrived: %v", err)
	}
	return msg
}

func dlqDepth(t *testing.T, srv *natstest.Server) uint64 {
	t.Helper()
	return streamInfo(t, srv, nats.DLQStream).State.Msgs
}

func TestConsumeRetriesUntilTheHandlerSucceeds(t *testing.T) {
	srv := ensured(t)
	attempts := make(chan nats.Delivery, 10)
	consume(t, srv, fastSpec("retry-ok"), func(_ context.Context, d nats.Delivery) error {
		attempts <- d
		if d.Attempt < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err := srv.Client.Publish(t.Context(), "agenthub.message.created", "evt-1", []byte("hi")); err != nil {
		t.Fatal(err)
	}

	for want := 1; want <= 3; want++ {
		d := recv(t, attempts)
		if d.Attempt != want || d.EventID != "evt-1" || string(d.Data) != "hi" || d.Subject != "agenthub.message.created" {
			t.Fatalf("delivery %d = %+v", want, d)
		}
	}
	if n := dlqDepth(t, srv); n != 0 {
		t.Errorf("DLQ holds %d entries after a success, want 0", n)
	}
}

func TestConsumeDeadLettersAfterTheLastAttempt(t *testing.T) {
	srv := ensured(t)
	attempts := make(chan int, 10)
	consume(t, srv, fastSpec("exhaust"), func(_ context.Context, d nats.Delivery) error {
		attempts <- d.Attempt
		return errors.New("downstream refused, sent Authorization: Bearer sk.live.secret")
	})
	if err := srv.Client.Publish(t.Context(), "agenthub.message.created", "evt-2", []byte(`{"id":"evt-2"}`)); err != nil {
		t.Fatal(err)
	}

	dl := nextDeadLetter(t, srv)
	if len(attempts) != 3 {
		t.Errorf("handler ran %d times before dead-lettering, want 3", len(attempts))
	}
	h := dl.Headers()
	if dl.Subject() != "dlq.agenthub.message.created" || string(dl.Data()) != `{"id":"evt-2"}` {
		t.Errorf("dead letter on %q with %q", dl.Subject(), dl.Data())
	}
	want := map[string]string{
		nats.HeaderSubject:    "agenthub.message.created",
		nats.HeaderEventID:    "evt-2",
		nats.HeaderConsumer:   "exhaust",
		nats.HeaderStreamSeq:  "1",
		nats.HeaderAttempts:   "3",
		jetstream.MsgIDHeader: "dlq:exhaust:1",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if e := h.Get(nats.HeaderError); !strings.Contains(e, "downstream refused") || strings.Contains(e, "sk.live.secret") {
		t.Errorf("error header = %q, want the message with the bearer token masked", e)
	}
}

func TestConsumeDeadLettersAPermanentErrorOnTheFirstAttempt(t *testing.T) {
	srv := ensured(t)
	attempts := make(chan int, 10)
	consume(t, srv, fastSpec("permanent"), func(_ context.Context, d nats.Delivery) error {
		attempts <- d.Attempt
		return nats.Permanent(errors.New("payload fails the schema"))
	})
	if err := srv.Client.Publish(t.Context(), "agenthub.message.created", "evt-3", []byte("bad")); err != nil {
		t.Fatal(err)
	}

	dl := nextDeadLetter(t, srv)
	if got := dl.Headers().Get(nats.HeaderAttempts); got != "1" || len(attempts) != 1 {
		t.Errorf("attempts header %q, handler calls %d; want 1 and 1", got, len(attempts))
	}
}

func TestConsumeRecoversAHandlerPanicAsARetry(t *testing.T) {
	srv := ensured(t)
	attempts := make(chan int, 10)
	consume(t, srv, fastSpec("panics"), func(_ context.Context, d nats.Delivery) error {
		attempts <- d.Attempt
		if d.Attempt == 1 {
			panic("boom")
		}
		return nil
	})
	if err := srv.Client.Publish(t.Context(), "agenthub.message.created", "evt-4", nil); err != nil {
		t.Fatal(err)
	}
	if a, b := recv(t, attempts), recv(t, attempts); a != 1 || b != 2 {
		t.Errorf("attempts = %d, %d; want 1, 2", a, b)
	}
}

func TestReplayDeliversTheEventAgainAndRemovesTheEntry(t *testing.T) {
	srv := ensured(t)
	failing := make(chan struct{})
	deliveries := make(chan nats.Delivery, 10)
	consume(t, srv, fastSpec("replay"), func(_ context.Context, d nats.Delivery) error {
		deliveries <- d
		select {
		case <-failing:
			return nil
		default:
			return nats.Permanent(errors.New("not yet"))
		}
	})
	if err := srv.Client.Publish(t.Context(), "agenthub.message.created", "evt-5", []byte("again")); err != nil {
		t.Fatal(err)
	}
	recv(t, deliveries)
	dl := nextDeadLetter(t, srv)
	meta, err := dl.Metadata()
	if err != nil {
		t.Fatal(err)
	}

	list, err := srv.Client.DeadLetters(t.Context(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Seq != meta.Sequence.Stream || list[0].EventID != "evt-5" || list[0].Consumer != "replay" ||
		list[0].Attempts != 1 || list[0].Error != "not yet" || list[0].Size != 5 || list[0].FailedAt.IsZero() {
		t.Fatalf("DeadLetters = %+v", list)
	}

	close(failing)
	if err := srv.Client.ReplayDeadLetter(t.Context(), list[0].Seq); err != nil {
		t.Fatal(err)
	}
	d := recv(t, deliveries)
	if d.EventID != "evt-5" || string(d.Data) != "again" || d.Attempt != 1 {
		t.Errorf("replayed delivery = %+v, want evt-5 / again / attempt 1", d)
	}
	if list, _ := srv.Client.DeadLetters(t.Context(), 0, 10); len(list) != 0 {
		t.Errorf("DLQ still lists %+v after replay", list)
	}
	if err := srv.Client.ReplayDeadLetter(t.Context(), meta.Sequence.Stream); !errors.Is(err, nats.ErrDeadLetterNotFound) {
		t.Errorf("second replay = %v, want ErrDeadLetterNotFound", err)
	}
}

func TestDeadLettersPagesInSequenceOrder(t *testing.T) {
	srv := ensured(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := srv.Client.Publish(t.Context(), "dlq.agenthub.x."+id, "dlq:"+id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := srv.Client.DeadLetters(t.Context(), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Seq != 1 || first[1].Seq != 2 {
		t.Fatalf("first page = %+v", first)
	}
	rest, err := srv.Client.DeadLetters(t.Context(), first[1].Seq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Seq != 3 {
		t.Errorf("second page = %+v", rest)
	}
}

func TestDeadLettersIsEmptyBeforeTheStreamExists(t *testing.T) {
	srv := natstest.New(t)
	list, err := srv.Client.DeadLetters(t.Context(), 0, 10)
	if err != nil || len(list) != 0 {
		t.Errorf("DeadLetters without a stream = %v, %v; want empty, nil", list, err)
	}
	if err := srv.Client.ReplayDeadLetter(t.Context(), 1); !errors.Is(err, nats.ErrDeadLetterNotFound) {
		t.Errorf("ReplayDeadLetter without a stream = %v, want ErrDeadLetterNotFound", err)
	}
}
