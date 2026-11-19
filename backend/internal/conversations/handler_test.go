package conversations_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/conversations"
)

type response struct {
	Data json.RawMessage `json:"data"`
	Meta *struct {
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

type conversationJSON struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	ProjectID    *string `json:"project_id"`
	Title        string  `json:"title"`
	CreatedBy    string  `json:"created_by"`
	LastSeq      *int64  `json:"last_seq"`
	CreatedAt    string  `json:"created_at"`
	Participants []struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		JoinedAt string `json:"joined_at"`
	} `json:"participants"`
}

func router(f fixture, user *auth.Principal) http.Handler {
	v1 := chi.NewRouter()
	conversations.NewHandler(f.svc).Routes(v1)
	r := chi.NewRouter()
	r.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), *user))
		}
		v1.ServeHTTP(w, req)
	}))
	return r
}

func do(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, response) {
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

func create(t *testing.T, f fixture, body string) conversationJSON {
	t.Helper()
	rec, resp := do(t, router(f, &f.user), http.MethodPost, "/api/v1/conversations", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var c conversationJSON
	if err := json.Unmarshal(resp.Data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreateOverHTTP(t *testing.T) {
	f := newFixture(t)
	c := create(t, f, `{"workspace_id":"`+f.workspace.String()+`","project_id":"`+f.project.String()+
		`","title":"Release","created_by":"`+uuid.NewString()+`"}`)

	if c.WorkspaceID != f.workspace.String() || c.ProjectID == nil || *c.ProjectID != f.project.String() || c.Title != "Release" {
		t.Errorf("conversation = %+v, want Release in the project", c)
	}
	if c.CreatedBy != f.user.ID.String() || c.LastSeq == nil || *c.LastSeq != 0 || uuid.Validate(c.ID) != nil || c.CreatedAt == "" {
		t.Errorf("conversation = %+v, want id, created_at, last_seq 0, created by the signed-in user", c)
	}
	if len(c.Participants) != 1 || c.Participants[0].Type != "user" || c.Participants[0].ID != f.user.ID.String() {
		t.Errorf("participants = %+v, want the creator", c.Participants)
	}
}

func TestCreateOverHTTPWithoutAProjectReturnsNull(t *testing.T) {
	f := newFixture(t)
	rec, resp := do(t, router(f, &f.user), http.MethodPost, "/api/v1/conversations", `{"workspace_id":"`+f.workspace.String()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	var raw map[string]any
	_ = json.Unmarshal(resp.Data, &raw)
	if v, ok := raw["project_id"]; !ok || v != nil {
		t.Errorf("project_id = %v (present %v), want null", v, ok)
	}
}

func TestCreateOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	valid := `{"workspace_id":"` + f.workspace.String() + `"}`
	tests := []struct {
		name   string
		caller *auth.Principal
		body   string
		status int
		field  string
	}{
		{"no principal", nil, valid, http.StatusUnauthorized, ""},
		{"non-member", &f.stranger, valid, http.StatusNotFound, ""},
		{"malformed JSON", &f.user, `{"workspace_id":`, http.StatusUnprocessableEntity, ""},
		{"bad workspace id", &f.user, `{"workspace_id":"acme"}`, http.StatusUnprocessableEntity, "workspace_id"},
		{"bad project id", &f.user, `{"workspace_id":"` + f.workspace.String() + `","project_id":"api"}`, http.StatusUnprocessableEntity, "project_id"},
		{"missing workspace", &f.user, `{}`, http.StatusUnprocessableEntity, "workspace_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := do(t, router(f, tt.caller), http.MethodPost, "/api/v1/conversations", tt.body)
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.field != "" && resp.Error.Details.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", resp.Error.Details.Fields, tt.field)
			}
		})
	}
}

func TestGetOverHTTP(t *testing.T) {
	f := newFixture(t)
	c := create(t, f, `{"workspace_id":"`+f.workspace.String()+`"}`)

	rec, resp := do(t, router(f, &f.user), http.MethodGet, "/api/v1/conversations/"+c.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	var got conversationJSON
	_ = json.Unmarshal(resp.Data, &got)
	if got.ID != c.ID || len(got.Participants) != 1 {
		t.Errorf("got %+v, want %s with its creator", got, c.ID)
	}

	for name, tc := range map[string]struct {
		caller *auth.Principal
		path   string
		status int
	}{
		"non-member": {&f.stranger, c.ID, http.StatusNotFound},
		"unknown":    {&f.user, uuid.NewString(), http.StatusNotFound},
		"not a uuid": {&f.user, "abc", http.StatusNotFound},
		"anonymous":  {nil, c.ID, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := do(t, router(f, tc.caller), http.MethodGet, "/api/v1/conversations/"+tc.path, "")
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
		})
	}
}

func TestListOverHTTPPagesWithAnOpaqueCursor(t *testing.T) {
	f := newFixture(t)
	for range 3 {
		create(t, f, `{"workspace_id":"`+f.workspace.String()+`"}`)
	}
	base := "/api/v1/conversations?limit=2&workspace_id=" + f.workspace.String()

	rec, first := do(t, router(f, &f.user), http.MethodGet, base, "")
	if rec.Code != http.StatusOK || first.Meta == nil || !first.Meta.HasMore || first.Meta.NextCursor == "" {
		t.Fatalf("first page: status %d, meta %+v (%s)", rec.Code, first.Meta, rec.Body)
	}
	var items []conversationJSON
	_ = json.Unmarshal(first.Data, &items)
	if len(items) != 2 || items[0].Participants != nil {
		t.Errorf("first page = %+v, want 2 items without participants", items)
	}

	rec, second := do(t, router(f, &f.user), http.MethodGet, base+"&cursor="+first.Meta.NextCursor, "")
	_ = json.Unmarshal(second.Data, &items)
	if rec.Code != http.StatusOK || len(items) != 1 || second.Meta.HasMore || second.Meta.NextCursor != "" {
		t.Errorf("second page: status %d, %d items, meta %+v, want 1 item and the end", rec.Code, len(items), second.Meta)
	}
}

func TestListOverHTTPRejects(t *testing.T) {
	f := newFixture(t)
	ws := "workspace_id=" + f.workspace.String()
	tests := []struct {
		name   string
		caller *auth.Principal
		query  string
		status int
	}{
		{"no workspace", &f.user, "", http.StatusUnprocessableEntity},
		{"bad workspace", &f.user, "workspace_id=acme", http.StatusUnprocessableEntity},
		{"bad project", &f.user, ws + "&project_id=api", http.StatusUnprocessableEntity},
		{"limit too big", &f.user, ws + "&limit=101", http.StatusUnprocessableEntity},
		{"garbage cursor", &f.user, ws + "&cursor=%25%25", http.StatusUnprocessableEntity},
		{"cursor of another shape", &f.user, ws + "&cursor=WzFd", http.StatusUnprocessableEntity},
		{"non-member", &f.stranger, ws, http.StatusNotFound},
		{"anonymous", nil, ws, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := do(t, router(f, tt.caller), http.MethodGet, "/api/v1/conversations?"+tt.query, "")
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
		})
	}
}
