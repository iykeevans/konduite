//go:build integration

package nats_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/platform/natstest"
)

func streamInfo(t *testing.T, srv *natstest.Server, name string) *jetstream.StreamInfo {
	t.Helper()
	s, err := srv.JetStream.Stream(t.Context(), name)
	if err != nil {
		t.Fatalf("stream %s: %v", name, err)
	}
	info, err := s.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func ensured(t *testing.T) *natstest.Server {
	t.Helper()
	srv := natstest.New(t)
	if err := srv.Client.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestEnsureStreamsCreatesBothStreams(t *testing.T) {
	srv := ensured(t)

	main := streamInfo(t, srv, nats.Stream).Config
	if len(main.Subjects) != 1 || main.Subjects[0] != "agenthub.>" || main.Storage != jetstream.FileStorage || main.Duplicates != 2*time.Minute {
		t.Errorf("AGENTHUB = subjects %v, storage %v, duplicates %v; want agenthub.>, file, 2m", main.Subjects, main.Storage, main.Duplicates)
	}
	dlq := streamInfo(t, srv, nats.DLQStream).Config
	if len(dlq.Subjects) != 1 || dlq.Subjects[0] != "dlq.agenthub.>" || dlq.Storage != jetstream.FileStorage {
		t.Errorf("AGENTHUB_DLQ = subjects %v, storage %v; want dlq.agenthub.>, file", dlq.Subjects, dlq.Storage)
	}
}

func TestEnsureStreamsIsIdempotentAndRepairsDrift(t *testing.T) {
	srv := natstest.New(t)
	if _, err := srv.JetStream.CreateStream(t.Context(), jetstream.StreamConfig{
		Name: nats.Stream, Subjects: []string{"agenthub.>"}, Storage: jetstream.FileStorage, Duplicates: 10 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	for i := range 2 {
		if err := srv.Client.EnsureStreams(t.Context()); err != nil {
			t.Fatalf("EnsureStreams run %d: %v", i+1, err)
		}
	}
	if d := streamInfo(t, srv, nats.Stream).Config.Duplicates; d != 2*time.Minute {
		t.Errorf("duplicates = %v after EnsureStreams, want 2m", d)
	}
}

func TestPublishDropsADuplicateMessageIDWithinTheWindow(t *testing.T) {
	srv := ensured(t)

	for _, id := range []string{"evt-1", "evt-1", "evt-2"} {
		if err := srv.Client.Publish(t.Context(), "agenthub.message.created", id, []byte(id)); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	if n := streamInfo(t, srv, nats.Stream).State.Msgs; n != 2 {
		t.Errorf("AGENTHUB holds %d messages, want 2 (the repeated evt-1 dropped)", n)
	}
}

func TestPublishFailsForASubjectNoStreamCaptures(t *testing.T) {
	srv := ensured(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := srv.Client.Publish(ctx, "elsewhere.thing", "evt-1", nil); err == nil {
		t.Error("publish outside agenthub.>: want an error, not a silent core-NATS publish")
	}
}

func TestPublishToADLQSubjectLandsInTheDLQStream(t *testing.T) {
	srv := ensured(t)
	if err := srv.Client.Publish(t.Context(), "dlq.agenthub.message.created", "evt-1", []byte("poison")); err != nil {
		t.Fatal(err)
	}
	if n := streamInfo(t, srv, nats.DLQStream).State.Msgs; n != 1 {
		t.Errorf("AGENTHUB_DLQ holds %d messages, want 1", n)
	}
	if n := streamInfo(t, srv, nats.Stream).State.Msgs; n != 0 {
		t.Errorf("AGENTHUB holds %d messages, want 0", n)
	}
}

func TestConsumerDeliversToADurablePullConsumer(t *testing.T) {
	srv := ensured(t)
	ctx := t.Context()
	cfg := jetstream.ConsumerConfig{Durable: "deliveries", FilterSubject: "agenthub.message.>"}
	if _, err := srv.Client.Consumer(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := srv.Client.Publish(ctx, "agenthub.message.created", "evt-1", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	cons, err := srv.Client.Consumer(ctx, cfg)
	if err != nil {
		t.Fatalf("second Consumer call with the same config: %v", err)
	}
	if p := cons.CachedInfo().Config.AckPolicy; p != jetstream.AckExplicitPolicy {
		t.Errorf("ack policy = %v, want explicit", p)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if string(msg.Data()) != "hello" || msg.Headers().Get(jetstream.MsgIDHeader) != "evt-1" {
		t.Errorf("got %q with id %q, want hello / evt-1", msg.Data(), msg.Headers().Get(jetstream.MsgIDHeader))
	}
	if err := msg.Ack(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPassesOnlyOnceTheStreamExists(t *testing.T) {
	srv := natstest.New(t)
	check := nats.Check(srv.Client)
	if err := check(t.Context()); err == nil {
		t.Error("Check before EnsureStreams: want an error")
	}
	if err := srv.Client.EnsureStreams(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := check(t.Context()); err != nil {
		t.Errorf("Check after EnsureStreams: %v", err)
	}
}
