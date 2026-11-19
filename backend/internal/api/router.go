// Package api assembles the HTTP handler for the API process.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
)

// Deps are the collaborators the router needs.
type Deps struct {
	// ReadinessChecks are run by /readyz, keyed by dependency name.
	ReadinessChecks map[string]Check
	// Authenticate guards every /api/v1 route. When nil, /api/v1 rejects all
	// requests, so forgetting to wire it can never leave the API open.
	Authenticate func(http.Handler) http.Handler
	// Routes are mounted under /api/v1, one func per domain.
	Routes []func(chi.Router)
	// PublicRoutes are mounted at the root with no user authentication.
	// Only routes that authenticate the caller themselves belong here, such
	// as the runtime token exchange, which takes a refresh credential.
	PublicRoutes []func(chi.Router)
}

// NewRouter returns the API's root handler.
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(log.Middleware, recoverer)
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(deps.ReadinessChecks))
	r.NotFound(notFound)
	authenticate := deps.Authenticate
	if authenticate == nil {
		authenticate = denyAll
	}
	// Wrap the whole subrouter rather than calling v1.Use: chi skips a
	// subrouter's middleware while it has no routes, which would leave
	// /api/v1 unauthenticated until the first domain mounts one.
	v1 := chi.NewRouter()
	v1.NotFound(notFound)
	for _, mount := range deps.Routes {
		mount(v1)
	}
	r.Mount("/api/v1", authenticate(v1))
	for _, mount := range deps.PublicRoutes {
		mount(r)
	}
	return r
}

func denyAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Fail(w, http.StatusUnauthorized, "unauthenticated", "authentication is not configured", nil)
	})
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	httpx.Fail(w, http.StatusNotFound, "not_found", "route not found", nil)
}
