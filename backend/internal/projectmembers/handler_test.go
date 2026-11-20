package projectmembers_test

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
	"github.com/iykeevans/konduite/backend/internal/projectmembers"
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

type memberJSON struct {
	UserID        string  `json:"user_id"`
	Role          string  `json:"role"`
	Email         *string `json:"email"`
	WorkspaceRole string  `json:"workspace_role"`
	JoinedAt      string  `json:"joined_at"`
}

func router(f fixture, caller *auth.Principal) http.Handler {
	v1 := chi.NewRouter()
	projectmembers.NewHandler(f.svc).Routes(v1)
	r := chi.NewRouter()
	r.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if caller != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), *caller))
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
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
		}
	}
	return rec, resp
}

func (f fixture) membersURL() string { return "/api/v1/projects/" + f.project.String() + "/members" }

func (f fixture) memberURL(id uuid.UUID) string { return f.membersURL() + "/" + id.String() }

func TestHandlerAddReturnsTheNewMember(t *testing.T) {
	f := newFixture(t)
	caller := user(f.maintainer)
	rec, resp := call(t, router(f, &caller), http.MethodPost, f.membersURL(),
		`{"user_id":"`+f.bystander.String()+`","role":"contributor"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var m memberJSON
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		t.Fatal(err)
	}
	if m.UserID != f.bystander.String() || m.Role != "contributor" || m.Email == nil ||
		m.WorkspaceRole != "developer" || m.JoinedAt == "" {
		t.Errorf("member = %+v", m)
	}
}

func TestHandlerAddStatuses(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		caller uuid.UUID
		body   string
		status int
		field  string
	}{
		{"already a member", f.maintainer, `{"user_id":"` + f.viewer.String() + `","role":"viewer"}`, http.StatusConflict, ""},
		{"outside the workspace", f.maintainer, `{"user_id":"` + f.outsider.String() + `","role":"viewer"}`, http.StatusUnprocessableEntity, "user_id"},
		{"malformed user id", f.maintainer, `{"user_id":"nope","role":"viewer"}`, http.StatusUnprocessableEntity, "user_id"},
		{"unknown role", f.maintainer, `{"user_id":"` + f.bystander.String() + `","role":"owner"}`, http.StatusUnprocessableEntity, "role"},
		{"not JSON", f.maintainer, `nope`, http.StatusUnprocessableEntity, ""},
		{"contributor", f.contributor, `{"user_id":"` + f.bystander.String() + `","role":"viewer"}`, http.StatusForbidden, ""},
		{"hidden project", f.bystander, `{"user_id":"` + f.bystander.String() + `","role":"viewer"}`, http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := user(tt.caller)
			rec, resp := call(t, router(f, &caller), http.MethodPost, f.membersURL(), tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.field != "" && resp.Error.Details.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a message for %s", resp.Error.Details.Fields, tt.field)
			}
		})
	}
}

func TestHandlerListPages(t *testing.T) {
	f := newFixture(t)
	caller := user(f.viewer)
	h := router(f, &caller)
	rec, resp := call(t, h, http.MethodGet, f.membersURL()+"?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	var first []memberJSON
	if err := json.Unmarshal(resp.Data, &first); err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || !resp.Meta.HasMore || resp.Meta.NextCursor == "" {
		t.Fatalf("first page = %+v, meta %+v", first, resp.Meta)
	}
	rec, resp = call(t, h, http.MethodGet, f.membersURL()+"?limit=2&cursor="+url.QueryEscape(resp.Meta.NextCursor), "")
	var second []memberJSON
	if err := json.Unmarshal(resp.Data, &second); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(second) != 1 || second[0].UserID != f.viewer.String() || resp.Meta.HasMore {
		t.Fatalf("second page = %+v, meta %+v", second, resp.Meta)
	}

	rec, _ = call(t, h, http.MethodGet, f.membersURL()+"?cursor=garbage", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad cursor status = %d, want 422", rec.Code)
	}
}

func TestHandlerChangeRoleAndRemove(t *testing.T) {
	f := newFixture(t)
	caller := user(f.maintainer)
	h := router(f, &caller)

	rec, resp := call(t, h, http.MethodPatch, f.memberURL(f.viewer), `{"role":"contributor"}`)
	var m memberJSON
	_ = json.Unmarshal(resp.Data, &m)
	if rec.Code != http.StatusOK || m.Role != "contributor" {
		t.Fatalf("patch status = %d, member %+v", rec.Code, m)
	}
	rec, _ = call(t, h, http.MethodPatch, f.memberURL(f.maintainer), `{"role":"viewer"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("demoting the last maintainer: status = %d, want 409", rec.Code)
	}
	rec, _ = call(t, h, http.MethodDelete, f.memberURL(f.viewer), "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", rec.Code)
	}
	rec, _ = call(t, h, http.MethodDelete, f.memberURL(f.viewer), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", rec.Code)
	}
	rec, _ = call(t, h, http.MethodDelete, f.membersURL()+"/not-a-uuid", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("malformed user id status = %d, want 404", rec.Code)
	}
}

func TestHandlerMalformedProjectIDIsNotFound(t *testing.T) {
	f := newFixture(t)
	caller := user(f.owner)
	rec, _ := call(t, router(f, &caller), http.MethodGet, "/api/v1/projects/nope/members", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandlerRequiresAPrincipal(t *testing.T) {
	f := newFixture(t)
	rec, _ := call(t, router(f, nil), http.MethodGet, f.membersURL(), "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
