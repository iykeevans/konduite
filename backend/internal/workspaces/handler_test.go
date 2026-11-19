package workspaces_test

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
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/workspaces"
)

type response struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"meta"`
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Fields map[string]string `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

type workspaceJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Role      string `json:"role"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// router mounts the handler the way cmd/api does, behind a stand-in for the
// Supabase middleware that signs the caller in as user (or nobody).
func router(f fixture, user *auth.Principal) http.Handler {
	v1 := chi.NewRouter()
	workspaces.NewHandler(f.svc).Routes(v1)
	r := chi.NewRouter()
	r.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), *user))
		}
		v1.ServeHTTP(w, req)
	}))
	return r
}

func call(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, response) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return rec, resp
}

func decodeData[T any](t *testing.T, resp response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		t.Fatalf("data: %v (%s)", err, resp.Data)
	}
	return v
}

func TestCreateOverHTTP(t *testing.T) {
	f := newFixture(t)
	rec, resp := call(t, router(f, &f.user), http.MethodPost, "/api/v1/workspaces",
		`{"name":"Acme","slug":"acme","created_by":"`+uuid.NewString()+`"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	ws := decodeData[workspaceJSON](t, resp)
	if ws.Name != "Acme" || ws.Slug != "acme" || ws.Role != "owner" || ws.CreatedBy != f.user.ID.String() {
		t.Errorf("data = %+v, want acme owned and created by the signed-in user", ws)
	}
	if uuid.Validate(ws.ID) != nil || ws.CreatedAt == "" || ws.UpdatedAt == "" {
		t.Errorf("data = %+v, want id and timestamps", ws)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/workspaces/"+ws.ID {
		t.Errorf("Location = %q, want the new workspace's URL", loc)
	}
}

func TestCreateOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	f.create(t, "Taken", "taken")
	tests := []struct {
		name   string
		caller *auth.Principal
		body   string
		status int
		code   string
	}{
		{"no principal", nil, `{"name":"Acme","slug":"acme"}`, http.StatusUnauthorized, "unauthenticated"},
		{"malformed JSON", &f.user, `{"name":`, http.StatusUnprocessableEntity, "invalid"},
		{"bad slug", &f.user, `{"name":"Acme","slug":"Acme!"}`, http.StatusUnprocessableEntity, "invalid"},
		{"taken slug", &f.user, `{"name":"Acme","slug":"taken"}`, http.StatusConflict, "conflict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := call(t, router(f, tt.caller), http.MethodPost, "/api/v1/workspaces", tt.body)
			if rec.Code != tt.status || resp.Error.Code != tt.code {
				t.Errorf("status = %d, code = %q, want %d %s (%s)", rec.Code, resp.Error.Code, tt.status, tt.code, rec.Body)
			}
		})
	}
}

func TestListOverHTTPPagesWithAnOpaqueCursor(t *testing.T) {
	f := newFixture(t)
	for _, slug := range []string{"alpha", "bravo", "charlie"} {
		f.create(t, slug, slug)
	}
	h := router(f, &f.user)

	rec, resp := call(t, h, http.MethodGet, "/api/v1/workspaces?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	first := decodeData[[]workspaceJSON](t, resp)
	if len(first) != 2 || first[0].Slug != "alpha" || first[1].Slug != "bravo" || !resp.Meta.HasMore {
		t.Fatalf("first page = %+v meta %+v, want alpha, bravo and more", first, resp.Meta)
	}
	if strings.Contains(resp.Meta.NextCursor, "bravo") {
		t.Errorf("next_cursor %q exposes the slug, want an opaque cursor", resp.Meta.NextCursor)
	}

	_, resp = call(t, h, http.MethodGet, "/api/v1/workspaces?limit=2&cursor="+url.QueryEscape(resp.Meta.NextCursor), "")
	second := decodeData[[]workspaceJSON](t, resp)
	if len(second) != 1 || second[0].Slug != "charlie" || resp.Meta.HasMore || resp.Meta.NextCursor != "" {
		t.Errorf("second page = %+v meta %+v, want only charlie and no more", second, resp.Meta)
	}
}

func TestListOverHTTPWithNoWorkspacesIsAnEmptyArray(t *testing.T) {
	f := newFixture(t)
	rec, resp := call(t, router(f, &f.user), http.MethodGet, "/api/v1/workspaces", "")
	if rec.Code != http.StatusOK || string(resp.Data) != "[]" {
		t.Errorf("status = %d, data = %s, want 200 and []", rec.Code, resp.Data)
	}
}

func TestListOverHTTPRejectsBadPagination(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{"limit=0", "limit=101", "cursor=%21%21"} {
		t.Run(q, func(t *testing.T) {
			rec, resp := call(t, router(f, &f.user), http.MethodGet, "/api/v1/workspaces?"+q, "")
			if rec.Code != http.StatusUnprocessableEntity || resp.Error.Code != "invalid" {
				t.Errorf("status = %d, code = %q, want 422 invalid", rec.Code, resp.Error.Code)
			}
		})
	}
}

func TestGetOverHTTP(t *testing.T) {
	f := newFixture(t)
	ws := f.create(t, "Acme", "acme")
	stranger := newUser()
	tests := []struct {
		name   string
		caller *auth.Principal
		path   string
		status int
	}{
		{"member", &f.user, ws.ID.String(), http.StatusOK},
		{"non-member", &stranger, ws.ID.String(), http.StatusNotFound},
		{"unknown id", &f.user, uuid.NewString(), http.StatusNotFound},
		{"not a uuid", &f.user, "acme", http.StatusNotFound},
		{"no principal", nil, ws.ID.String(), http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := call(t, router(f, tt.caller), http.MethodGet, "/api/v1/workspaces/"+tt.path, "")
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.status == http.StatusOK {
				if got := decodeData[workspaceJSON](t, resp); got.ID != ws.ID.String() || got.Role != "owner" {
					t.Errorf("data = %+v, want the workspace seen by its owner", got)
				}
			}
		})
	}
}

func TestUpdateOverHTTP(t *testing.T) {
	f := newFixture(t)
	ws := f.create(t, "Acme", "acme")
	f.create(t, "Taken", "taken")
	dev := newUser()
	f.repo.addMember(ws.ID, dev.ID, authz.RoleDeveloper)
	tests := []struct {
		name   string
		caller *auth.Principal
		body   string
		status int
		code   string
	}{
		{"developer", &dev, `{"name":"Mine"}`, http.StatusForbidden, "forbidden"},
		{"empty patch", &f.user, `{}`, http.StatusUnprocessableEntity, "invalid"},
		{"taken slug", &f.user, `{"slug":"taken"}`, http.StatusConflict, "conflict"},
		{"owner", &f.user, `{"name":"Acme Inc","slug":"acme-inc"}`, http.StatusOK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := call(t, router(f, tt.caller), http.MethodPatch, "/api/v1/workspaces/"+ws.ID.String(), tt.body)
			if rec.Code != tt.status || resp.Error.Code != tt.code {
				t.Fatalf("status = %d, code = %q, want %d %q (%s)", rec.Code, resp.Error.Code, tt.status, tt.code, rec.Body)
			}
			if tt.status == http.StatusOK {
				if got := decodeData[workspaceJSON](t, resp); got.Name != "Acme Inc" || got.Slug != "acme-inc" {
					t.Errorf("data = %+v, want renamed to Acme Inc / acme-inc", got)
				}
			}
		})
	}
}
