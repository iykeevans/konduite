// Package api assembles the HTTP handler for the API process.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

// Deps are the collaborators the router needs.
type Deps struct {
	// ReadinessChecks are run by /readyz, keyed by dependency name.
	ReadinessChecks map[string]Check
	// Routes are mounted under /api/v1, one func per domain.
	Routes []func(chi.Router)
}

// NewRouter returns the API's root handler.
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer)
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(deps.ReadinessChecks))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Fail(w, http.StatusNotFound, "not_found", "route not found", nil)
	})
	r.Route("/api/v1", func(v1 chi.Router) {
		for _, mount := range deps.Routes {
			mount(v1)
		}
	})
	return r
}
