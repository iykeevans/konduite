// Command api runs the Konduite HTTP API.
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iykeevans/konduite/backend/internal/api"
	"github.com/iykeevans/konduite/backend/internal/platform/config"
	"github.com/iykeevans/konduite/backend/internal/platform/server"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("api exited", "error", err)
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

	srv := &http.Server{
		Handler:           api.NewRouter(api.Deps{}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	slog.Info("api listening", "addr", ln.Addr().String(), "env", cfg.Env)
	if err := server.Serve(ctx, srv, ln, cfg.ShutdownTimeout); err != nil {
		return err
	}
	slog.Info("api stopped")
	return nil
}
