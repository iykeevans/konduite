package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/iykeevans/konduite/backend/internal/api"
	"github.com/iykeevans/konduite/backend/internal/platform/log"
)

func TestHealthChecksGetARequestID(t *testing.T) {
	rec, _ := get(t, api.NewRouter(api.Deps{}), "/healthz")

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID response header is missing")
	}
}

func TestPanicIsLoggedWithTheRequestID(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(log.New(&buf, slog.LevelInfo))
	t.Cleanup(func() { slog.SetDefault(prev) })

	deps := api.Deps{Authenticate: allowAll, Routes: []func(chi.Router){
		func(r chi.Router) {
			r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
		},
	}}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/boom", nil)
	req.Header.Set("X-Request-ID", "req-42")
	rec := httptest.NewRecorder()
	api.NewRouter(deps).ServeHTTP(rec, req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("panic log is not one JSON line: %v (%q)", err, buf.String())
	}
	if entry["request_id"] != "req-42" {
		t.Errorf("request_id = %v, want req-42", entry["request_id"])
	}
	if got := rec.Header().Get("X-Request-ID"); got != "req-42" {
		t.Errorf("X-Request-ID = %q, want req-42", got)
	}
}
