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

func TestRevokeEndpointDeletesTheCredentialFromTheBody(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)
	body := `{"refresh_credential":"` + cred + `"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/runtime/revoke", strings.NewReader(body))
	router(f, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body %q, want 204 with no body", rec.Code, rec.Body)
	}

	rec, resp := post(t, router(f, nil), "/v1/runtime/token", body)
	if rec.Code != http.StatusUnauthorized || resp.Error.Code != "unauthenticated" {
		t.Errorf("token after revoke: status = %d, want 401", rec.Code)
	}
}

func TestRevokeEndpointRejects(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)
	tests := []struct {
		name   string
		target string
		body   string
		status int
	}{
		{"wrong credential", "/v1/runtime/revoke", `{"refresh_credential":"krt_nope"}`, http.StatusUnauthorized},
		{"missing credential", "/v1/runtime/revoke", `{}`, http.StatusUnprocessableEntity},
		{"credential only in the URL", "/v1/runtime/revoke?refresh_credential=" + url.QueryEscape(cred), `{}`, http.StatusUnprocessableEntity},
		{"not JSON", "/v1/runtime/revoke", `nope`, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := post(t, router(f, nil), tt.target, tt.body)
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
		})
	}
	if len(f.repo.credentials) != 1 {
		t.Errorf("%d credentials left, want the real one kept", len(f.repo.credentials))
	}
}

func get(t *testing.T, h http.Handler, target, bearer string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return rec, body
}

func assertNoCredential(t *testing.T, rec *httptest.ResponseRecorder, cred string) {
	t.Helper()
	body := rec.Body.String()
	if strings.Contains(body, cred) || strings.Contains(body, "refresh_credential") || strings.Contains(body, "secret") {
		t.Errorf("response exposes credential material: %s", body)
	}
}

func TestGetRuntimeOverHTTP(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)
	_, list := get(t, router(f, &f.user), "/api/v1/runtimes?workspace_id="+f.workspace.String(), "")
	id := list["data"].([]any)[0].(map[string]any)["id"].(string)

	rec, body := get(t, router(f, &f.user), "/api/v1/runtimes/"+id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	d := body["data"].(map[string]any)
	if d["id"] != id || d["workspace_id"] != f.workspace.String() || d["owner_id"] != f.user.ID.String() ||
		d["name"] != "laptop" || d["revoked"] != false || d["created_at"] == nil {
		t.Errorf("data = %v, want the registered laptop", d)
	}
	if _, ok := d["credential_last_used_at"]; !ok {
		t.Errorf("data = %v, want credential_last_used_at (null until first exchange)", d)
	}
	assertNoCredential(t, rec, cred)
}

func TestGetRuntimeOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	register(t, f)
	_, list := get(t, router(f, &f.user), "/api/v1/runtimes?workspace_id="+f.workspace.String(), "")
	id := list["data"].([]any)[0].(map[string]any)["id"].(string)
	stranger := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}

	tests := []struct {
		name   string
		caller *auth.Principal
		target string
		status int
	}{
		{"no principal", nil, "/api/v1/runtimes/" + id, http.StatusUnauthorized},
		{"non-member", &stranger, "/api/v1/runtimes/" + id, http.StatusNotFound},
		{"unknown id", &f.user, "/api/v1/runtimes/" + uuid.NewString(), http.StatusNotFound},
		{"malformed id", &f.user, "/api/v1/runtimes/laptop", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := get(t, router(f, tt.caller), tt.target, "")
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
		})
	}
}

func TestListRuntimesOverHTTPPages(t *testing.T) {
	f := newFixture(t)
	creds := []string{register(t, f), register(t, f), register(t, f)}
	h := router(f, &f.user)

	rec, body := get(t, h, "/api/v1/runtimes?limit=2&workspace_id="+f.workspace.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	meta := body["meta"].(map[string]any)
	if len(body["data"].([]any)) != 2 || meta["has_more"] != true || meta["next_cursor"] == "" {
		t.Fatalf("first page = %s, want 2 items and a cursor", rec.Body)
	}
	for _, c := range creds {
		assertNoCredential(t, rec, c)
	}

	rec, body = get(t, h, "/api/v1/runtimes?limit=2&workspace_id="+f.workspace.String()+
		"&cursor="+url.QueryEscape(meta["next_cursor"].(string)), "")
	if rec.Code != http.StatusOK || len(body["data"].([]any)) != 1 || body["meta"].(map[string]any)["has_more"] != false {
		t.Errorf("second page = %d %s, want 1 item and no more", rec.Code, rec.Body)
	}
}

func TestListRuntimesOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	stranger := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	ws := f.workspace.String()
	tests := []struct {
		name   string
		caller *auth.Principal
		query  string
		status int
	}{
		{"no principal", nil, "workspace_id=" + ws, http.StatusUnauthorized},
		{"non-member", &stranger, "workspace_id=" + ws, http.StatusNotFound},
		{"no workspace", &f.user, "", http.StatusUnprocessableEntity},
		{"bad workspace", &f.user, "workspace_id=acme", http.StatusUnprocessableEntity},
		{"limit too big", &f.user, "limit=101&workspace_id=" + ws, http.StatusUnprocessableEntity},
		{"bad cursor", &f.user, "cursor=nope&workspace_id=" + ws, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := get(t, router(f, tt.caller), "/api/v1/runtimes?"+tt.query, "")
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
		})
	}
}

func TestSelfEndpointReturnsTheTokensRuntime(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)
	_, tok := post(t, router(f, nil), "/v1/runtime/token", `{"refresh_credential":"`+cred+`"}`)
	access, _ := tok.Data["access_token"].(string)

	rec, body := get(t, router(f, nil), "/v1/runtime/self", access)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	d := body["data"].(map[string]any)
	if d["workspace_id"] != f.workspace.String() || d["name"] != "laptop" || d["revoked"] != false {
		t.Errorf("data = %v, want the registered laptop", d)
	}
	assertNoCredential(t, rec, cred)
}

func TestSelfEndpointRejects(t *testing.T) {
	f := newFixture(t)
	cred := register(t, f)
	for name, bearer := range map[string]string{
		"no token":           "",
		"refresh credential": cred,
		"garbage":            "nope",
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := get(t, router(f, nil), "/v1/runtime/self", bearer)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 (%s)", rec.Code, rec.Body)
			}
		})
	}
}
