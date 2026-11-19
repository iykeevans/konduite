// Package nats is Konduite's JetStream client: it owns the stream
// definitions, publishes with a message id for dedupe, and binds durable
// pull consumers to the AGENTHUB stream.
//
// Request handlers must not publish. Events reach NATS through the outbox
// relay so the write and its event commit together.
package nats

import (
	"context"
	"errors"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// Stream holds every domain event, on subjects under agenthub.>.
	Stream = "AGENTHUB"
	// DLQStream holds events a consumer gave up on, on subjects under
	// dlq.agenthub.>. JetStream rejects streams with overlapping subjects,
	// so the DLQ prefix must stay outside agenthub.>.
	DLQStream = "AGENTHUB_DLQ"

	// DedupeWindow is how long JetStream remembers a message id. A relay
	// retry later than this publishes a duplicate, so consumers still
	// dedupe by event id.
	DedupeWindow = 2 * time.Minute
)

func streamConfigs() []jetstream.StreamConfig {
	return []jetstream.StreamConfig{
		{
			Name:       Stream,
			Subjects:   []string{"agenthub.>"},
			Storage:    jetstream.FileStorage,
			Retention:  jetstream.LimitsPolicy,
			Duplicates: DedupeWindow,
			MaxAge:     7 * 24 * time.Hour,
		},
		{
			Name:       DLQStream,
			Subjects:   []string{"dlq.agenthub.>"},
			Storage:    jetstream.FileStorage,
			Retention:  jetstream.LimitsPolicy,
			Duplicates: DedupeWindow,
			MaxAge:     30 * 24 * time.Hour,
		},
	}
}

// Client is a NATS connection with JetStream. It is safe for concurrent use.
type Client struct {
	nc *natsgo.Conn
	js jetstream.JetStream
}

// Connect returns a client for url, naming the connection name in server
// monitoring. It does not wait for the server: the connection keeps
// retrying in the background, so a process can start and report "not
// ready" while NATS is still coming up.
func Connect(url, name string) (*Client, error) {
	nc, err := natsgo.Connect(url,
		natsgo.Name(name),
		natsgo.RetryOnFailedConnect(true),
		natsgo.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	return &Client{nc: nc, js: js}, nil
}

// Close closes the connection without draining.
func (c *Client) Close() {
	c.nc.Close()
}

// EnsureStreams creates the AGENTHUB and AGENTHUB_DLQ streams, or updates
// them to the configuration this package defines. It is safe to run on
// every start.
func (c *Client) EnsureStreams(ctx context.Context) error {
	for _, cfg := range streamConfigs() {
		if _, err := c.js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("ensure stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

// Publish stores data on subject and waits for the stream's ack. msgID is
// sent as Nats-Msg-Id, so a repeat within DedupeWindow is acked without
// being stored again; pass the event id. A subject outside both streams
// is an error, never a fire-and-forget core publish.
func (c *Client) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	if msgID == "" {
		return errors.New("nats: publish needs a message id")
	}
	if _, err := c.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// Consumer creates or updates a durable pull consumer on the AGENTHUB
// stream and returns it. cfg.Durable is required. The zero AckPolicy is
// explicit ack, which at-least-once delivery relies on.
func (c *Client) Consumer(ctx context.Context, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	if cfg.Durable == "" {
		return nil, errors.New("nats: consumer needs a durable name")
	}
	cons, err := c.js.CreateOrUpdateConsumer(ctx, Stream, cfg)
	if err != nil {
		return nil, fmt.Errorf("consumer %s: %w", cfg.Durable, err)
	}
	return cons, nil
}

// Check returns a readiness check that passes when the connection is up
// and the AGENTHUB stream answers.
func Check(c *Client) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if !c.nc.IsConnected() {
			return errors.New("nats: not connected")
		}
		_, err := c.js.Stream(ctx, Stream)
		return err
	}
}
