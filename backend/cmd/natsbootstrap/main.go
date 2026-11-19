// Command natsbootstrap creates or updates Konduite's JetStream streams and
// exits. docker compose runs it as a one-shot job; the worker runs the same
// code on start, so the stream definitions live only in internal/platform/nats.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("nats bootstrap failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = natsgo.DefaultURL
	}
	client, err := nats.Connect(url, "konduite-natsbootstrap")
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.EnsureStreams(ctx); err != nil {
		return err
	}
	slog.Info("nats streams ready", "streams", []string{nats.Stream, nats.DLQStream})
	return nil
}
