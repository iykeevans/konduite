// Command api runs the Konduite HTTP API.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/admin"
	"github.com/iykeevans/konduite/backend/internal/api"
	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/deadletters"
	"github.com/iykeevans/konduite/backend/internal/members"
	"github.com/iykeevans/konduite/backend/internal/platform/config"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/platform/server"
	"github.com/iykeevans/konduite/backend/internal/projects"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/backend/internal/workspaces"
)

func main() {
	slog.SetDefault(log.New(os.Stdout, slog.LevelInfo))
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

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	bus, err := nats.Connect(cfg.NATSURL, "konduite-api")
	if err != nil {
		return err
	}
	defer bus.Close()

	tokenKey, ok := cfg.RuntimeTokenKey.Key()
	if !ok {
		if _, tokenKey, err = ed25519.GenerateKey(rand.Reader); err != nil {
			return err
		}
		slog.Warn("RUNTIME_TOKEN_SIGNING_KEY not set: signing runtime tokens with a throwaway key, so they stop working on restart")
	}
	az := newAuthorizer(pool)
	runtimeHandler := runtimes.NewHandler(runtimes.NewService(
		runtimes.NewPostgresRepository(pool), az, tokenKey))
	workspaceHandler := workspaces.NewHandler(workspaces.NewService(workspaces.NewPostgresRepository(pool), az))
	projectHandler := projects.NewHandler(projects.NewService(projects.NewPostgresRepository(pool), az))
	conversationHandler := conversations.NewHandler(conversations.NewService(
		conversations.NewPostgresRepository(pool), az))
	dlqHandler := deadletters.NewHandler(deadletters.NewService(bus, admin.NewAllowlist(cfg.PlatformAdmins)))
	memberHandler := members.NewHandler(members.NewService(members.NewPostgresRepository(pool), az))

	srv := &http.Server{
		Handler: api.NewRouter(api.Deps{
			ReadinessChecks: map[string]api.Check{"postgres": db.Check(pool)},
			Authenticate:    auth.Middleware(auth.NewVerifier(auth.SupabaseOptions(cfg.SupabaseURL))),
			Routes:          []func(chi.Router){runtimeHandler.Routes, workspaceHandler.Routes, projectHandler.Routes, conversationHandler.Routes, memberHandler.Routes, dlqHandler.Routes},
			PublicRoutes:    []func(chi.Router){runtimeHandler.TokenRoutes},
		}),
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

func newAuthorizer(pool *pgxpool.Pool) *authz.Authorizer {
	return authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))
}
