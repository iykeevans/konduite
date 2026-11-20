package agents

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
	"github.com/iykeevans/konduite/packages/contracts"
)

const maxBodyBytes = 16 << 10

// RuntimeVerifier checks a runtime access token and returns the runtime it
// was issued to.
type RuntimeVerifier func(token string) (Runtime, error)

// Handler serves /api/v1/agents and the runtime's own /v1/runtime/agents.
type Handler struct {
	reg    *Registry
	verify RuntimeVerifier
}

// NewHandler returns a Handler backed by reg that checks runtime access
// tokens with verify.
func NewHandler(reg *Registry, verify RuntimeVerifier) *Handler {
	return &Handler{reg: reg, verify: verify}
}

// Routes mounts the user-facing routes. They must sit behind the Supabase
// auth middleware (api.Deps.Routes).
func (h *Handler) Routes(r chi.Router) {
	r.Post("/agents", h.register)
	r.Get("/agents", h.list)
	r.Get("/agents/{agentID}", h.get)
	r.Patch("/agents/{agentID}", h.update)
	r.Delete("/agents/{agentID}", h.delete)
}

// RuntimeRoutes mounts the routes a runtime calls with its own access token.
// They must not sit behind the Supabase auth middleware
// (api.Deps.PublicRoutes).
func (h *Handler) RuntimeRoutes(r chi.Router) {
	r.Post("/v1/runtime/agents", h.registerForRuntime)
	r.Get("/v1/runtime/agents", h.listForRuntime)
}

type agentResponse struct {
	ID           uuid.UUID                   `json:"id"`
	WorkspaceID  uuid.UUID                   `json:"workspace_id"`
	RuntimeID    uuid.UUID                   `json:"runtime_id"`
	Provider     string                      `json:"provider"`
	Name         string                      `json:"name"`
	DisplayName  *string                     `json:"display_name"`
	Description  *string                     `json:"description"`
	Version      *string                     `json:"version"`
	Capabilities contracts.AgentCapabilities `json:"capabilities"`
	Status       Status                      `json:"status"`
	LastSeenAt   time.Time                   `json:"last_seen_at"`
	CreatedAt    time.Time                   `json:"created_at"`
	UpdatedAt    time.Time                   `json:"updated_at"`
}

func toResponse(a Agent) agentResponse {
	return agentResponse{
		ID: a.ID, WorkspaceID: a.WorkspaceID, RuntimeID: a.RuntimeID, Provider: a.Provider, Name: a.Name,
		DisplayName: nullable(a.DisplayName), Description: nullable(a.Description), Version: nullable(a.Version),
		Capabilities: a.Capabilities, Status: a.Status, LastSeenAt: a.LastSeenAt, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func toResponses(as []Agent) []agentResponse {
	out := make([]agentResponse, len(as))
	for i, a := range as {
		out[i] = toResponse(a)
	}
	return out
}

type registerRequest struct {
	RuntimeID    string                      `json:"runtime_id"`
	Provider     string                      `json:"provider"`
	Name         string                      `json:"name"`
	DisplayName  string                      `json:"display_name"`
	Description  string                      `json:"description"`
	Capabilities contracts.AgentCapabilities `json:"capabilities"`
}

func (r registerRequest) input() RegisterInput {
	return RegisterInput{
		Provider: r.Provider, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Capabilities: r.Capabilities,
	}
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req registerRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	runtimeID, err := uuid.Parse(req.RuntimeID)
	if err != nil && req.RuntimeID != "" {
		httpx.Error(w, r, errs.Invalid("invalid agent registration", map[string]string{"runtime_id": "must be a UUID"}))
		return
	}
	agent, err := h.reg.Register(r.Context(), p, runtimeID, req.input())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/agents/"+agent.ID.String())
	httpx.JSON(w, http.StatusCreated, toResponse(agent))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	fields := map[string]string{}
	in := ListInput{Limit: page.Limit}
	query := r.URL.Query()
	for param, dst := range map[string]*uuid.UUID{"workspace_id": &in.WorkspaceID, "runtime_id": &in.RuntimeID} {
		if raw := query.Get(param); raw != "" {
			if *dst, err = uuid.Parse(raw); err != nil {
				fields[param] = "must be a UUID"
			}
		}
	}
	if page.Cursor != "" {
		var pos Position
		if err := page.DecodeCursor(&pos); err != nil || pos.ID == uuid.Nil || pos.CreatedAt.IsZero() {
			fields["cursor"] = "is not a valid cursor"
		} else {
			in.After = &pos
		}
	}
	if len(fields) > 0 {
		httpx.Error(w, r, errs.Invalid("invalid agent query", fields))
		return
	}
	res, err := h.reg.List(r.Context(), p, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(res.Next)
	}
	httpx.JSONPage(w, toResponses(res.Items), next)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, id, ok := principalAndID(w, r)
	if !ok {
		return
	}
	agent, err := h.reg.Get(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(agent))
}

type updateRequest struct {
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, id, ok := principalAndID(w, r)
	if !ok {
		return
	}
	var req updateRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	agent, err := h.reg.Update(r.Context(), p, id, UpdateInput(req))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(agent))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := principalAndID(w, r)
	if !ok {
		return
	}
	if err := h.reg.Delete(r.Context(), p, id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) registerForRuntime(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.runtime(w, r)
	if !ok {
		return
	}
	var req registerRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	agent, err := h.reg.RegisterForRuntime(r.Context(), rt, req.input())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/agents/"+agent.ID.String())
	httpx.JSON(w, http.StatusCreated, toResponse(agent))
}

func (h *Handler) listForRuntime(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.runtime(w, r)
	if !ok {
		return
	}
	agents, err := h.reg.ListForRuntime(r.Context(), rt)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponses(agents))
}

func (h *Handler) runtime(w http.ResponseWriter, r *http.Request) (Runtime, bool) {
	denied := errs.Unauthenticated("invalid runtime access token")
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		httpx.Error(w, r, errs.Unauthenticated("missing runtime access token"))
		return Runtime{}, false
	}
	rt, err := h.verify(token)
	if err != nil || rt.ID == uuid.Nil || rt.WorkspaceID == uuid.Nil {
		httpx.Error(w, r, denied)
		return Runtime{}, false
	}
	return rt, true
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
	}
	return p, ok
}

// A malformed id gets the same 404 as an agent the caller cannot see.
func principalAndID(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, ok := principal(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "agentID"))
	if err != nil {
		httpx.Error(w, r, errs.NotFound("agent not found"))
		return auth.Principal{}, uuid.Nil, false
	}
	return p, id, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		return errs.Invalid("request body must be a JSON object", nil)
	}
	return nil
}
