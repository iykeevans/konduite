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
	rec, body := get(t, api.NewRouter(api.Deps{Authenticate: allowAll}), "/api/v1/nope")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	e, _ := body["error"].(map[string]any)
	if e["code"] != "not_found" {
		t.Errorf("error.code = %v, want not_found", e["code"])
	}
}

func TestPanicReturnsInternalErrorWithoutLeaking(t *testing.T) {
	deps := api.Deps{Authenticate: allowAll, Routes: []func(chi.Router){
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

func allowAll(next http.Handler) http.Handler { return next }

func TestAPIRoutesGoThroughAuthenticationButHealthChecksDoNot(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated"}}`))
		})
	}
	deps := api.Deps{Authenticate: deny, Routes: []func(chi.Router){
		func(r chi.Router) {
			r.Get("/secret", func(http.ResponseWriter, *http.Request) { t.Error("handler ran") })
		},
	}}
	h := api.NewRouter(deps)

	for _, path := range []string{"/api/v1/secret", "/api/v1/nope"} {
		if rec, _ := get(t, h, path); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, rec.Code)
		}
	}
	if rec, _ := get(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz: status = %d, want 200 without credentials", rec.Code)
	}
}

func TestPublicRoutesSkipUserAuthentication(t *testing.T) {
	deps := api.Deps{PublicRoutes: []func(chi.Router){
		func(r chi.Router) {
			r.Get("/v1/runtime/ping", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{}}`))
			})
		},
	}}
	if rec, _ := get(t, api.NewRouter(deps), "/v1/runtime/ping"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 without a user token", rec.Code)
	}
}

func TestAPIRoutesFailClosedWhenNoAuthenticatorIsWired(t *testing.T) {
	deps := api.Deps{Routes: []func(chi.Router){
		func(r chi.Router) {
			r.Get("/secret", func(http.ResponseWriter, *http.Request) { t.Error("handler ran") })
		},
	}}
	rec, body := get(t, api.NewRouter(deps), "/api/v1/secret")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if e, _ := body["error"].(map[string]any); e["code"] != "unauthenticated" {
		t.Errorf("error = %v, want unauthenticated", body["error"])
	}
}

func TestAPIRoutesRequireAuthenticationEvenBeforeAnyDomainMountsRoutes(t *testing.T) {
	ran := false
	authenticate := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			ran = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated"}}`))
		})
	}
	rec, _ := get(t, api.NewRouter(api.Deps{Authenticate: authenticate}), "/api/v1/workspaces")
	if rec.Code != http.StatusUnauthorized || !ran {
		t.Errorf("status = %d, authenticator ran = %v; want 401 from the authenticator", rec.Code, ran)
	}
}
