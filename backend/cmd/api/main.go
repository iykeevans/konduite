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
	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/api"
	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/deadletters"
	"github.com/iykeevans/konduite/backend/internal/gateway"
	"github.com/iykeevans/konduite/backend/internal/members"
	"github.com/iykeevans/konduite/backend/internal/messages"
	"github.com/iykeevans/konduite/backend/internal/platform/config"
	"github.com/iykeevans/konduite/backend/internal/platform/db"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
	"github.com/iykeevans/konduite/backend/internal/platform/server"
	"github.com/iykeevans/konduite/backend/internal/projectmembers"
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
	conversationService := conversations.NewService(conversations.NewPostgresRepository(pool), az)
	conversationHandler := conversations.NewHandler(conversationService)
	messageHandler := messages.NewHandler(messages.NewService(messages.NewPostgresRepository(pool), conversationService))
	dlqHandler := deadletters.NewHandler(deadletters.NewService(bus, admin.NewAllowlist(cfg.PlatformAdmins)))
	memberHandler := members.NewHandler(members.NewService(members.NewPostgresRepository(pool), az))
	projectMemberHandler := projectmembers.NewHandler(projectmembers.NewService(projectmembers.NewPostgresRepository(pool), az))
	agentHandler := agents.NewHandler(agents.NewRegistry(agents.NewPostgresRegistryRepository(pool), az),
		runtimeVerifier(tokenKey.Public().(ed25519.PublicKey)))
	tokenPub := tokenKey.Public().(ed25519.PublicKey)
	runtimeGateway := gateway.New(gateway.Config{
		Verify:     func(token string) (runtimes.Claims, error) { return runtimes.VerifyAccessToken(tokenPub, token) },
		Agents:     agents.NewService(agents.NewPostgresRepository(pool)),
		Subscriber: bus,
	})

	srv := &http.Server{
		Handler: api.NewRouter(api.Deps{
			ReadinessChecks: map[string]api.Check{"postgres": db.Check(pool)},
			Authenticate:    auth.Middleware(auth.NewVerifier(auth.SupabaseOptions(cfg.SupabaseURL))),
			Routes:          []func(chi.Router){runtimeHandler.Routes, workspaceHandler.Routes, projectHandler.Routes, conversationHandler.Routes, messageHandler.Routes, memberHandler.Routes, projectMemberHandler.Routes, dlqHandler.Routes, agentHandler.Routes},
			PublicRoutes:    []func(chi.Router){runtimeHandler.TokenRoutes, agentHandler.RuntimeRoutes, runtimeGateway.Routes},
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	slog.Info("api listening", "addr", ln.Addr().String(), "env", cfg.Env)
	serveErr := server.Serve(ctx, srv, ln, cfg.ShutdownTimeout)
	gwCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := runtimeGateway.Shutdown(gwCtx); err != nil {
		slog.Warn("runtime connections did not close in time", "error", err)
	}
	if serveErr != nil {
		return serveErr
	}
	slog.Info("api stopped")
	return nil
}

func newAuthorizer(pool *pgxpool.Pool) *authz.Authorizer {
	return authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))
}

func runtimeVerifier(pub ed25519.PublicKey) agents.RuntimeVerifier {
	return func(token string) (agents.Runtime, error) {
		c, err := runtimes.VerifyAccessToken(pub, token)
		return agents.Runtime{ID: c.RuntimeID, WorkspaceID: c.WorkspaceID}, err
	}
}
