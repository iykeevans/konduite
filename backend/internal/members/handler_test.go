package members_test

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
	"github.com/iykeevans/konduite/backend/internal/members"
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
	UserID   string  `json:"user_id"`
	Role     string  `json:"role"`
	Email    *string `json:"email"`
	JoinedAt string  `json:"joined_at"`
}

type invitationJSON struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	InvitedBy   string `json:"invited_by"`
	ExpiresAt   string `json:"expires_at"`
	Token       string `json:"token"`
}

func router(f fixture, user *auth.Principal) http.Handler {
	v1 := chi.NewRouter()
	members.NewHandler(f.svc).Routes(v1)
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
	if rec.Code != http.StatusNoContent {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
		}
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

func (f fixture) path(rest string) string { return "/api/v1/workspaces/" + f.ws.String() + rest }

func TestInviteAndAcceptOverHTTP(t *testing.T) {
	f := newFixture(t)
	rec, resp := call(t, router(f, &f.owner), http.MethodPost, f.path("/invitations"),
		`{"email":"Ada@example.com","role":"developer","invited_by":"`+uuid.NewString()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite status = %d (%s)", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the response carrying the token may be cached")
	}
	inv := decodeData[invitationJSON](t, resp)
	if inv.Email != "ada@example.com" || inv.Role != "developer" || inv.InvitedBy != f.owner.ID.String() ||
		inv.WorkspaceID != f.ws.String() || !strings.HasPrefix(inv.Token, "kdi_") || inv.ExpiresAt == "" {
		t.Fatalf("invitation = %+v", inv)
	}

	ada := newUser("ada@example.com")
	rec, resp = call(t, router(f, &ada), http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+inv.Token+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept status = %d (%s)", rec.Code, rec.Body)
	}
	joined := decodeData[struct {
		WorkspaceID string `json:"workspace_id"`
		Role        string `json:"role"`
	}](t, resp)
	if joined.WorkspaceID != f.ws.String() || joined.Role != "developer" {
		t.Errorf("joined = %+v", joined)
	}

	rec, resp = call(t, router(f, &ada), http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+inv.Token+`"}`)
	if rec.Code != http.StatusNotFound || resp.Error.Code != "not_found" {
		t.Errorf("second accept = %d %q, want 404", rec.Code, resp.Error.Code)
	}
}

func TestInvitationListNeverIncludesTokens(t *testing.T) {
	f := newFixture(t)
	f.invite(t, "ada@example.com", dev)

	rec, resp := call(t, router(f, &f.owner), http.MethodGet, f.path("/invitations"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "token") || strings.Contains(rec.Body.String(), "kdi_") {
		t.Errorf("list leaks a token: %s", rec.Body)
	}
	items := decodeData[[]invitationJSON](t, resp)
	if len(items) != 1 || items[0].Email != "ada@example.com" {
		t.Errorf("items = %+v", items)
	}
}

func TestMembersOverHTTP(t *testing.T) {
	f := newFixture(t)
	devP := f.member(dev, "dev@example.com")
	f.member(pm, "")

	rec, resp := call(t, router(f, &devP), http.MethodGet, f.path("/members?limit=2"), "")
	if rec.Code != http.StatusOK || !resp.Meta.HasMore || resp.Meta.NextCursor == "" {
		t.Fatalf("first page = %d %+v (%s)", rec.Code, resp.Meta, rec.Body)
	}
	first := decodeData[[]memberJSON](t, resp)
	if len(first) != 2 || first[0].UserID != f.owner.ID.String() || first[0].Role != "owner" {
		t.Errorf("first page = %+v", first)
	}
	_, resp = call(t, router(f, &devP), http.MethodGet, f.path("/members?limit=2&cursor="+url.QueryEscape(resp.Meta.NextCursor)), "")
	second := decodeData[[]memberJSON](t, resp)
	if len(second) != 1 || second[0].Email != nil || resp.Meta.HasMore {
		t.Errorf("second page = %+v meta %+v, want the project manager with a null email", second, resp.Meta)
	}

	rec, resp = call(t, router(f, &f.owner), http.MethodPatch, f.path("/members/"+devP.ID.String()), `{"role":"admin"}`)
	if rec.Code != http.StatusOK || decodeData[memberJSON](t, resp).Role != "admin" {
		t.Errorf("change role = %d (%s)", rec.Code, rec.Body)
	}

	rec, _ = call(t, router(f, &f.owner), http.MethodDelete, f.path("/members/"+devP.ID.String()), "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("remove = %d (%s)", rec.Code, rec.Body)
	}
}

func TestHTTPErrorStatuses(t *testing.T) {
	f := newFixture(t)
	devP := f.member(dev, "dev@example.com")
	inv := f.invite(t, "ada@example.com", dev)

	tests := []struct {
		name         string
		caller       *auth.Principal
		method, path string
		body         string
		want         int
	}{
		{"signed out", nil, http.MethodGet, f.path("/members"), "", http.StatusUnauthorized},
		{"malformed workspace id", &f.owner, http.MethodGet, "/api/v1/workspaces/nope/members", "", http.StatusNotFound},
		{"malformed member id", &f.owner, http.MethodDelete, f.path("/members/nope"), "", http.StatusNotFound},
		{"malformed invitation id", &f.owner, http.MethodDelete, f.path("/invitations/nope"), "", http.StatusNotFound},
		{"developer invites", &devP, http.MethodPost, f.path("/invitations"), `{"email":"x@example.com","role":"developer"}`, http.StatusForbidden},
		{"invite owner role", &f.owner, http.MethodPost, f.path("/invitations"), `{"email":"x@example.com","role":"owner"}`, http.StatusUnprocessableEntity},
		{"invite existing member", &f.owner, http.MethodPost, f.path("/invitations"), `{"email":"dev@example.com","role":"admin"}`, http.StatusConflict},
		{"demote last owner", &f.owner, http.MethodPatch, f.path("/members/" + f.owner.ID.String()), `{"role":"admin"}`, http.StatusConflict},
		{"bad body", &f.owner, http.MethodPost, f.path("/invitations"), `nope`, http.StatusUnprocessableEntity},
		{"bad cursor", &f.owner, http.MethodGet, f.path("/members?cursor=%25%25"), "", http.StatusUnprocessableEntity},
		{"accept wrong email", &devP, http.MethodPost, "/api/v1/invitations/accept", `{"token":"` + inv.Token + `"}`, http.StatusForbidden},
		{"accept signed out", nil, http.MethodPost, "/api/v1/invitations/accept", `{"token":"` + inv.Token + `"}`, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := call(t, router(f, tt.caller), tt.method, tt.path, tt.body)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
