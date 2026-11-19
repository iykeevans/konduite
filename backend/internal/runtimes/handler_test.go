package runtimes_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
)

type response struct {
	Data  map[string]any `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Fields map[string]string `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

// router mounts the handler the way cmd/api does: Routes behind a stand-in
// for the Supabase middleware that signs the caller in as user (or nobody),
// TokenRoutes with no authentication at all.
func router(f fixture, user *auth.Principal) http.Handler {
	h := runtimes.NewHandler(f.svc)
	v1 := chi.NewRouter()
	h.Routes(v1)
	r := chi.NewRouter()
	r.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), *user))
		}
		v1.ServeHTTP(w, req)
	}))
	h.TokenRoutes(r)
	return r
}

func post(t *testing.T, h http.Handler, target, body string) (*httptest.ResponseRecorder, response) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return rec, resp
}

func register(t *testing.T, f fixture) string {
	t.Helper()
	rec, resp := post(t, router(f, &f.user), "/api/v1/runtimes/register",
		`{"workspace_id":"`+f.workspace.String()+`","name":"laptop"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	cred, _ := resp.Data["refresh_credential"].(string)
	return cred
}

func TestRegisterOverHTTPReturnsTheCredentialOnce(t *testing.T) {
	f := newFixture(t)
	rec, resp := post(t, router(f, &f.user), "/api/v1/runtimes/register",
		`{"workspace_id":"`+f.workspace.String()+`","name":"laptop","owner_id":"`+uuid.NewString()+`"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	d := resp.Data
	if d["owner_id"] != f.user.ID.String() || d["workspace_id"] != f.workspace.String() || d["name"] != "laptop" {
		t.Errorf("data = %v, want laptop in the workspace, owned by the signed-in user", d)
	}
	if cred, _ := d["refresh_credential"].(string); !strings.HasPrefix(cred, "krt_") {
		t.Errorf("refresh_credential = %v, want a krt_ credential", d["refresh_credential"])
	}
	if id, _ := d["runtime_id"].(string); uuid.Validate(id) != nil || d["created_at"] == nil {
		t.Errorf("data = %v, want runtime_id and created_at", d)
	}
}

func TestRegisterOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	stranger := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	valid := `{"workspace_id":"` + f.workspace.String() + `","name":"laptop"}`
	tests := []struct {
		name   string
		caller *auth.Principal
		body   string
		status int
		code   string
	}{
		{"no principal", nil, valid, http.StatusUnauthorized, "unauthenticated"},
		{"non-member", &stranger, valid, http.StatusNotFound, "not_found"},
		{"malformed JSON", &f.user, `{"name":`, http.StatusUnprocessableEntity, "invalid"},
		{"bad workspace id", &f.user, `{"workspace_id":"acme","name":"laptop"}`, http.StatusUnprocessableEntity, "invalid"},
		{"missing name", &f.user, `{"workspace_id":"` + f.workspace.String() + `"}`, http.StatusUnprocessableEntity, "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := post(t, router(f, tt.caller), "/api/v1/runtimes/register", tt.body)
			if rec.Code != tt.status || resp.Error.Code != tt.code {
				t.Errorf("status = %d, code = %q, want %d %s (%s)", rec.Code, resp.Error.Code, tt.status, tt.code, rec.Body)
			}
		})
	}
}

func TestTokenEndpointExchangesTheCredentialFromTheBody(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)

	rec, resp := post(t, router(f, nil), "/v1/runtime/token", `{"refresh_credential":"`+cred+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	d := resp.Data
	if d["token_type"] != "Bearer" || d["expires_in"] != float64(900) || d["expires_at"] == nil {
		t.Errorf("data = %v, want a Bearer token expiring in 900s", d)
	}
	token, _ := d["access_token"].(string)
	claims, err := runtimes.VerifyAccessToken(f.pub, token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.OwnerID != f.user.ID || claims.WorkspaceID != f.workspace {
		t.Errorf("claims = %+v, want the registering user's runtime", claims)
	}
}

func TestTokenEndpointIgnoresACredentialInTheURL(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)

	rec, resp := post(t, router(f, nil), "/v1/runtime/token?refresh_credential="+url.QueryEscape(cred), `{}`)

	if rec.Code != http.StatusUnprocessableEntity || resp.Error.Details.Fields["refresh_credential"] == "" {
		t.Errorf("status = %d, body %s, want 422 asking for refresh_credential in the body", rec.Code, rec.Body)
	}
}

func TestTokenEndpointRejectsAWrongCredential(t *testing.T) {
	f := newFixture(t)
	rec, resp := post(t, router(f, nil), "/v1/runtime/token", `{"refresh_credential":"krt_nope"}`)
	if rec.Code != http.StatusUnauthorized || resp.Error.Code != "unauthenticated" {
		t.Errorf("status = %d, code = %q, want 401 unauthenticated", rec.Code, resp.Error.Code)
	}
}
