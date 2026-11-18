package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/iykeevans/konduite/backend/internal/api"
)

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return rec, body
}

func TestHealthzReportsOK(t *testing.T) {
	rec, body := get(t, api.NewRouter(api.Deps{}), "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	data, _ := body["data"].(map[string]any)
	if data["status"] != "ok" {
		t.Errorf("body = %v, want data.status = ok", body)
	}
}

func TestReadyzFailsWhenACheckFails(t *testing.T) {
	deps := api.Deps{ReadinessChecks: map[string]api.Check{
		"postgres": func(context.Context) error { return errors.New("dial tcp: connection refused") },
		"nats":     func(context.Context) error { return nil },
	}}
	rec, body := get(t, api.NewRouter(deps), "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	e, _ := body["error"].(map[string]any)
	if e["code"] != "unavailable" {
		t.Errorf("error.code = %v, want unavailable", e["code"])
	}
	details, _ := e["details"].(map[string]any)
	failing, _ := details["failing"].([]any)
	if len(failing) != 1 || failing[0] != "postgres" {
		t.Errorf("error.details.failing = %v, want [postgres]", details["failing"])
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("response leaks the check's error text: %s", rec.Body.String())
	}
}

func TestReadyzPassesWhenAllChecksPass(t *testing.T) {
	deps := api.Deps{ReadinessChecks: map[string]api.Check{
		"postgres": func(context.Context) error { return nil },
	}}
	rec, body := get(t, api.NewRouter(deps), "/readyz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", rec.Code, body)
	}
}

func TestUnknownRouteReturnsNotFoundEnvelope(t *testing.T) {
	rec, body := get(t, api.NewRouter(api.Deps{}), "/api/v1/nope")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	e, _ := body["error"].(map[string]any)
	if e["code"] != "not_found" {
		t.Errorf("error.code = %v, want not_found", e["code"])
	}
}

func TestPanicReturnsInternalErrorWithoutLeaking(t *testing.T) {
	deps := api.Deps{Routes: []func(chi.Router){
		func(r chi.Router) {
			r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("secret: db password is hunter2") })
		},
	}}
	rec, body := get(t, api.NewRouter(deps), "/api/v1/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	e, _ := body["error"].(map[string]any)
	if e["code"] != "internal" {
		t.Errorf("error.code = %v, want internal", e["code"])
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("response leaks panic value: %s", rec.Body.String())
	}
}
