package agents_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/agents"
	"github.com/iykeevans/konduite/backend/internal/auth"
)

type response struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		NextCursor string `json:"next_cursor"`
	} `json:"meta"`
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Fields map[string]string `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

type agentJSON struct {
	ID           string          `json:"id"`
	RuntimeID    string          `json:"runtime_id"`
	Provider     string          `json:"provider"`
	Name         string          `json:"name"`
	DisplayName  *string         `json:"display_name"`
	Description  *string         `json:"description"`
	Version      *string         `json:"version"`
	Capabilities json.RawMessage `json:"capabilities"`
	Status       string          `json:"status"`
}

const goodToken = "good-token"

func (w world) router(caller *uuid.UUID) http.Handler {
	h := agents.NewHandler(w.reg, func(token string) (agents.Runtime, error) {
		if token != goodToken {
			return agents.Runtime{}, errors.New("bad token")
		}
		return agents.Runtime{ID: w.rt.ID, WorkspaceID: w.ws}, nil
	})
	v1 := chi.NewRouter()
	h.Routes(v1)
	r := chi.NewRouter()
	h.RuntimeRoutes(r)
	r.Mount("/api/v1", http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if caller != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), user(*caller)))
		}
		v1.ServeHTTP(rw, req)
	}))
	return r
}

func call(t *testing.T, h http.Handler, method, target, token, body string) (*httptest.ResponseRecorder, response) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(rec, req)
	var resp response
	if rec.Code != http.StatusNoContent {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not JSON: %v (%q)", err, rec.Body)
		}
	}
	return rec, resp
}

func TestRegisterOverHTTP(t *testing.T) {
	w := newWorld()
	rec, resp := call(t, w.router(&w.dev), http.MethodPost, "/api/v1/agents", "",
		`{"runtime_id":"`+w.rt.ID.String()+`","provider":"claude_code","name":"claude","display_name":"Reviewer","capabilities":{"supports_cancel":true}}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var a agentJSON
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatal(err)
	}
	if a.RuntimeID != w.rt.ID.String() || a.Provider != "claude_code" || a.Name != "claude" || a.Status != "offline" ||
		a.DisplayName == nil || *a.DisplayName != "Reviewer" || a.Description != nil || a.Version != nil ||
		string(a.Capabilities) != `{"supports_cancel":true}` {
		t.Errorf("data = %+v (%s)", a, resp.Data)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/agents/"+a.ID {
		t.Errorf("Location = %q", loc)
	}
}

func TestAgentRoutesReject(t *testing.T) {
	w := newWorld()
	agentURL := "/api/v1/agents/" + w.agent.ID.String()
	for _, tc := range []struct {
		name           string
		caller         *uuid.UUID
		method, target string
		body           string
		status         int
		code           string
	}{
		{"no principal", nil, http.MethodGet, agentURL, "", http.StatusUnauthorized, "unauthenticated"},
		{"bad json", &w.dev, http.MethodPost, "/api/v1/agents", `{`, http.StatusUnprocessableEntity, "invalid"},
		{"bad runtime id", &w.dev, http.MethodPost, "/api/v1/agents", `{"runtime_id":"x","provider":"echo","name":"echo"}`, http.StatusUnprocessableEntity, "invalid"},
		{"someone else's runtime", &w.owner, http.MethodPost, "/api/v1/agents", `{"runtime_id":"` + w.rt.ID.String() + `","provider":"echo","name":"echo"}`, http.StatusForbidden, "forbidden"},
		{"malformed id", &w.dev, http.MethodGet, "/api/v1/agents/nope", "", http.StatusNotFound, "not_found"},
		{"non-member get", &w.out, http.MethodGet, agentURL, "", http.StatusNotFound, "not_found"},
		{"non-owner patch", &w.pm, http.MethodPatch, agentURL, `{"display_name":"x"}`, http.StatusForbidden, "forbidden"},
		{"non-owner delete", &w.admin, http.MethodDelete, agentURL, "", http.StatusForbidden, "forbidden"},
		{"list without workspace", &w.dev, http.MethodGet, "/api/v1/agents", "", http.StatusUnprocessableEntity, "invalid"},
		{"list bad runtime id", &w.dev, http.MethodGet, "/api/v1/agents?workspace_id=" + w.ws.String() + "&runtime_id=x", "", http.StatusUnprocessableEntity, "invalid"},
		{"list bad cursor", &w.dev, http.MethodGet, "/api/v1/agents?workspace_id=" + w.ws.String() + "&cursor=zz", "", http.StatusUnprocessableEntity, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, resp := call(t, w.router(tc.caller), tc.method, tc.target, "", tc.body)
			if rec.Code != tc.status || resp.Error.Code != tc.code {
				t.Errorf("got %d %q, want %d %q (%s)", rec.Code, resp.Error.Code, tc.status, tc.code, rec.Body)
			}
		})
	}
}

func TestGetUpdateDeleteAndListOverHTTP(t *testing.T) {
	w := newWorld()
	h := w.router(&w.dev)
	agentURL := "/api/v1/agents/" + w.agent.ID.String()

	if rec, _ := call(t, h, http.MethodGet, agentURL, "", ""); rec.Code != http.StatusOK {
		t.Errorf("get = %d (%s)", rec.Code, rec.Body)
	}
	if rec, _ := call(t, h, http.MethodPatch, agentURL, "", `{"display_name":"Reviewer"}`); rec.Code != http.StatusOK {
		t.Errorf("patch = %d (%s)", rec.Code, rec.Body)
	}
	if got := w.repo.updated[0]; got.DisplayName == nil || *got.DisplayName != "Reviewer" || got.Description != nil {
		t.Errorf("update input = %+v, want only display_name", got)
	}
	if rec, _ := call(t, h, http.MethodDelete, agentURL, "", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d (%s)", rec.Code, rec.Body)
	}

	w.repo.list = []agents.Agent{w.agent}
	rec, resp := call(t, h, http.MethodGet, "/api/v1/agents?workspace_id="+w.ws.String()+"&runtime_id="+w.rt.ID.String(), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d (%s)", rec.Code, rec.Body)
	}
	var items []agentJSON
	if err := json.Unmarshal(resp.Data, &items); err != nil || len(items) != 1 || resp.Meta.NextCursor != "" {
		t.Errorf("list data = %s, cursor %q", resp.Data, resp.Meta.NextCursor)
	}
	if w.repo.filter.RuntimeID != w.rt.ID || w.repo.filter.WorkspaceID != w.ws {
		t.Errorf("filter = %+v", w.repo.filter)
	}
}

func TestRuntimeAgentRoutes(t *testing.T) {
	w := newWorld()
	h := w.router(nil)
	body := `{"provider":"echo","name":"echo","runtime_id":"` + w.foreignRT.ID.String() + `"}`

	for name, token := range map[string]string{"no token": "", "bad token": "forged"} {
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodGet} {
				rec, resp := call(t, h, method, "/v1/runtime/agents", token, body)
				if rec.Code != http.StatusUnauthorized || resp.Error.Code != "unauthenticated" {
					t.Errorf("%s = %d %q, want 401", method, rec.Code, resp.Error.Code)
				}
			}
			if w.repo.writes != 0 {
				t.Error("stored an agent without a valid runtime token")
			}
		})
	}

	rec, _ := call(t, h, http.MethodPost, "/v1/runtime/agents", goodToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d (%s)", rec.Code, rec.Body)
	}
	if got := w.repo.created[0].Runtime; got.ID != w.rt.ID {
		t.Errorf("registered on runtime %s, want the token's %s, never the body's", got.ID, w.rt.ID)
	}

	w.repo.list = []agents.Agent{w.agent}
	rec, resp := call(t, h, http.MethodGet, "/v1/runtime/agents", goodToken, "")
	var items []agentJSON
	if rec.Code != http.StatusOK || json.Unmarshal(resp.Data, &items) != nil || len(items) != 1 {
		t.Errorf("list = %d %s", rec.Code, resp.Data)
	}
}
