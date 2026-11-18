// Command worker runs Konduite's background consumers: outbox relay,
// message delivery, Slack sync and notifications as they are added.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iykeevans/konduite/backend/internal/platform/config"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
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

	reg := worker.NewRegistry()
	// Consumers are registered here as they land, starting with the outbox relay (MSG-004).

	slog.Info("worker started", "env", cfg.Env)
	if err := reg.Run(ctx, cfg.WorkerDrainTimeout); err != nil {
		return err
	}
	slog.Info("worker stopped")
	return nil
}
