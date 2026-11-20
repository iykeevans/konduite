package projects_test

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
	"github.com/iykeevans/konduite/backend/internal/projects"
)

type response struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"meta"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			Fields map[string]string `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

type projectJSON struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	RepositoryURL *string `json:"repository_url"`
	CreatedBy     string  `json:"created_by"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func router(f fixture, user *auth.Principal) http.Handler {
	v1 := chi.NewRouter()
	projects.NewHandler(f.svc).Routes(v1)
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

func (f fixture) projectsURL() string {
	return "/api/v1/workspaces/" + f.workspace.String() + "/projects"
}

func TestCreateOverHTTP(t *testing.T) {
	f := newFixture(t)
	rec, resp := call(t, router(f, &f.owner), http.MethodPost, f.projectsURL(),
		`{"name":"Konduite","description":"API","repository_url":"https://github.com/acme/k","created_by":"`+uuid.NewString()+`"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	p := decodeData[projectJSON](t, resp)
	if p.Name != "Konduite" || p.Description != "API" || p.RepositoryURL == nil || *p.RepositoryURL != "https://github.com/acme/k" ||
		p.WorkspaceID != f.workspace.String() || p.CreatedBy != f.owner.ID.String() || uuid.Validate(p.ID) != nil || p.CreatedAt == "" {
		t.Errorf("data = %+v, want Konduite created by the signed-in user", p)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/projects/"+p.ID {
		t.Errorf("Location = %q, want the new project's URL", loc)
	}
}

func TestCreateOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	f.create(t, "Taken")
	dev := f.member(authz.RoleDeveloper)
	stranger := newUser()
	tests := []struct {
		name   string
		caller *auth.Principal
		target string
		body   string
		status int
		code   string
	}{
		{"no principal", nil, f.projectsURL(), `{"name":"K"}`, http.StatusUnauthorized, "unauthenticated"},
		{"bad json", &f.owner, f.projectsURL(), `{`, http.StatusUnprocessableEntity, "invalid"},
		{"bad url", &f.owner, f.projectsURL(), `{"name":"K","repository_url":"https://a:b@x.io/r"}`, http.StatusUnprocessableEntity, "invalid"},
		{"taken name", &f.owner, f.projectsURL(), `{"name":"Taken"}`, http.StatusConflict, "conflict"},
		{"developer", &dev, f.projectsURL(), `{"name":"K"}`, http.StatusForbidden, "forbidden"},
		{"non-member", &stranger, f.projectsURL(), `{"name":"K"}`, http.StatusNotFound, "not_found"},
		{"malformed workspace id", &f.owner, "/api/v1/workspaces/nope/projects", `{"name":"K"}`, http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := call(t, router(f, tt.caller), http.MethodPost, tt.target, tt.body)
			if rec.Code != tt.status || resp.Error.Code != tt.code {
				t.Errorf("status = %d code %q, want %d %q (%s)", rec.Code, resp.Error.Code, tt.status, tt.code, rec.Body)
			}
		})
	}
}

func TestListOverHTTPPagesWithACursor(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		f.create(t, name)
	}
	h := router(f, &f.owner)

	rec, resp := call(t, h, http.MethodGet, f.projectsURL()+"?limit=2", "")
	if rec.Code != http.StatusOK || !resp.Meta.HasMore || resp.Meta.NextCursor == "" {
		t.Fatalf("status %d meta %+v, want 200 with a next cursor (%s)", rec.Code, resp.Meta, rec.Body)
	}
	if got := decodeData[[]projectJSON](t, resp); len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "bravo" {
		t.Errorf("first page = %+v, want alpha bravo", got)
	}
	rec, resp = call(t, h, http.MethodGet, f.projectsURL()+"?limit=2&cursor="+url.QueryEscape(resp.Meta.NextCursor), "")
	if got := decodeData[[]projectJSON](t, resp); rec.Code != http.StatusOK || len(got) != 1 || got[0].Name != "charlie" || resp.Meta.HasMore {
		t.Errorf("second page = %+v meta %+v, want only charlie", got, resp.Meta)
	}
	if got := decodeData[[]projectJSON](t, resp); got[0].RepositoryURL != nil {
		t.Errorf("repository_url = %v, want null when unset", *got[0].RepositoryURL)
	}
}

func TestListOverHTTPRejectsABadCursorOrLimit(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{"?cursor=!!", "?limit=0", "?limit=101"} {
		rec, resp := call(t, router(f, &f.owner), http.MethodGet, f.projectsURL()+q, "")
		if rec.Code != http.StatusUnprocessableEntity || resp.Error.Code != "invalid" {
			t.Errorf("%s: status = %d code %q, want 422", q, rec.Code, resp.Error.Code)
		}
	}
}

func TestGetAndUpdateOverHTTP(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "Konduite")
	h := router(f, &f.owner)

	rec, resp := call(t, h, http.MethodGet, "/api/v1/projects/"+p.ID.String(), "")
	if got := decodeData[projectJSON](t, resp); rec.Code != http.StatusOK || got.ID != p.ID.String() {
		t.Errorf("get: status %d data %+v, want 200 Konduite", rec.Code, got)
	}

	rec, resp = call(t, h, http.MethodPatch, "/api/v1/projects/"+p.ID.String(), `{"description":"Now with docs"}`)
	if got := decodeData[projectJSON](t, resp); rec.Code != http.StatusOK || got.Description != "Now with docs" || got.Name != "Konduite" {
		t.Errorf("patch: status %d data %+v, want 200 with the new description", rec.Code, got)
	}
}

func TestHiddenProjectsAreTheSame404OverHTTP(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "Konduite")
	dev := f.member(authz.RoleDeveloper)
	viewer := f.member(authz.RoleDeveloper)
	f.repo.addProjectMember(p.ID, viewer.ID, authz.ProjectRoleViewer)
	stranger := newUser()

	tests := []struct {
		name   string
		caller *auth.Principal
		method string
		target string
		body   string
		status int
	}{
		{"missing", &f.owner, http.MethodGet, "/api/v1/projects/" + uuid.NewString(), "", http.StatusNotFound},
		{"malformed id", &f.owner, http.MethodGet, "/api/v1/projects/nope", "", http.StatusNotFound},
		{"member without role", &dev, http.MethodGet, "/api/v1/projects/" + p.ID.String(), "", http.StatusNotFound},
		{"stranger", &stranger, http.MethodGet, "/api/v1/projects/" + p.ID.String(), "", http.StatusNotFound},
		{"stranger patch", &stranger, http.MethodPatch, "/api/v1/projects/" + p.ID.String(), `{"name":"x"}`, http.StatusNotFound},
		{"viewer patch", &viewer, http.MethodPatch, "/api/v1/projects/" + p.ID.String(), `{"name":"x"}`, http.StatusForbidden},
	}
	var message string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := call(t, router(f, tt.caller), tt.method, tt.target, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.status != http.StatusNotFound {
				return
			}
			if message == "" {
				message = resp.Error.Message
			}
			if resp.Error.Message != message {
				t.Errorf("message = %q, want %q like every other hidden project", resp.Error.Message, message)
			}
		})
	}
}
