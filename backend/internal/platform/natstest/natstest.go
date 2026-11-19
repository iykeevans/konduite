// Package natstest gives integration tests a private NATS server with
// JetStream enabled.
package natstest

import (
	"context"
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

const image = "nats:2.11-alpine" // matches docker-compose.yml

// Server is a running NATS container. Client is the code under test;
// JetStream is a raw handle for assertions and setup the client doesn't expose.
type Server struct {
	URL       string
	Client    *nats.Client
	JetStream jetstream.JetStream
}

// New starts a fresh server for t, so stream names never collide between
// tests. No streams exist until the test calls Client.EnsureStreams.
func New(t testing.TB) *Server {
	t.Helper()
	ctx := t.Context()
	c, err := tcnats.Run(ctx, image)
	if err != nil {
		t.Fatalf("natstest: start nats: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	url, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}

	client, err := nats.Connect(url, t.Name())
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}
	t.Cleanup(client.Close)

	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("natstest: raw connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}
	return &Server{URL: url, Client: client, JetStream: js}
}
