package nats_test

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

func unreachable(t *testing.T) *nats.Client {
	t.Helper()
	c, err := nats.Connect("nats://127.0.0.1:1", "test")
	if err != nil {
		t.Fatalf("Connect should not dial eagerly: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestPublishRequiresAMessageID(t *testing.T) {
	c := unreachable(t)
	if err := c.Publish(t.Context(), "agenthub.test", "", []byte("x")); err == nil {
		t.Error("Publish with an empty message id: want an error")
	}
}

func TestConsumerRequiresADurableName(t *testing.T) {
	c := unreachable(t)
	if _, err := c.Consumer(t.Context(), jetstream.ConsumerConfig{}); err == nil {
		t.Error("Consumer without a durable name: want an error")
	}
}

func TestCheckFailsWhileDisconnected(t *testing.T) {
	c := unreachable(t)
	if err := nats.Check(c)(t.Context()); err == nil {
		t.Error("Check while disconnected: want an error")
	}
}

func TestConnectRejectsAMalformedURL(t *testing.T) {
	if _, err := nats.Connect("nats://[::1", "test"); err == nil {
		t.Error("Connect with a malformed URL: want an error")
	}
}
