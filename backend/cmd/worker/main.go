// Command worker runs Konduite's background consumers: outbox relay,
// message delivery, Slack sync and notifications as they are added.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iykeevans/konduite/backend/internal/deliveries"
	"github.com/iykeevans/konduite/backend/internal/outbox"
	"github.com/iykeevans/konduite/backend/internal/platform/config"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/worker"
)

func main() {
	slog.SetDefault(log.New(os.Stdout, slog.LevelInfo))
	if err := run(); err != nil {
		slog.Error("worker exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	bus, err := nats.Connect(cfg.NATSURL, "konduite-worker")
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	reg := worker.NewRegistry()
	relay := outbox.NewRelay(pool, bus, outbox.Config{Logger: slog.Default()})
	if err := reg.Add("outbox-relay", relay.Run); err != nil {
		return err
	}
	router := deliveries.NewRouter(pool, slog.Default())
	if err := reg.Add("message-router", func(ctx context.Context) error {
		return bus.Consume(ctx, deliveries.ConsumerSpec(slog.Default()), router.Handle)
	}); err != nil {
		return err
	}

	slog.Info("worker started", "env", cfg.Env)
	if err := reg.Run(ctx, cfg.WorkerDrainTimeout); err != nil {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
