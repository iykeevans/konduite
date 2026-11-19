package workspaces

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

const maxBodyBytes = 16 << 10

// Handler serves /api/v1/workspaces.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the workspace routes. They must sit behind the Supabase auth
// middleware (api.Deps.Routes). Paths are registered one by one rather than
// with r.Route("/workspaces"), because chi panics when a second domain
// mounts its own routes under /workspaces/{workspaceID}.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces", h.create)
	r.Get("/workspaces", h.list)
	r.Get("/workspaces/{workspaceID}", h.get)
	r.Patch("/workspaces/{workspaceID}", h.update)
}

type workspaceResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Role      string    `json:"role"`
	CreatedBy uuid.UUID `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toResponse(ws Workspace) workspaceResponse {
	return workspaceResponse{
		ID: ws.ID, Name: ws.Name, Slug: ws.Slug, Role: string(ws.Role),
		CreatedBy: ws.CreatedBy, CreatedAt: ws.CreatedAt, UpdatedAt: ws.UpdatedAt,
	}
}

type createRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ws, err := h.svc.Create(r.Context(), p, CreateInput(req))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+ws.ID.String())
	httpx.JSON(w, http.StatusCreated, toResponse(ws))
}

type listCursor struct {
	After string `json:"after"`
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
	var cur listCursor
	if err := page.DecodeCursor(&cur); err != nil {
		httpx.Error(w, r, errs.Invalid("invalid pagination parameters", map[string]string{"cursor": "is not a valid cursor"}))
		return
	}
	res, err := h.svc.List(r.Context(), p, ListInput{After: cur.After, Limit: page.Limit})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]workspaceResponse, len(res.Items))
	for i, ws := range res.Items {
		items[i] = toResponse(ws)
	}
	var next string
	if res.Next != "" {
		next = httpx.EncodeCursor(listCursor{After: res.Next})
	}
	httpx.JSONPage(w, items, next)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := workspaceID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ws, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(ws))
}

type updateRequest struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := workspaceID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req updateRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ws, err := h.svc.Update(r.Context(), p, id, UpdateInput(req))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(ws))
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
	}
	return p, ok
}

// A malformed id gets the same 404 as a workspace the caller cannot see.
func workspaceID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "workspaceID"))
	if err != nil {
		return uuid.Nil, errs.NotFound("workspace not found")
	}
	return id, nil
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		return errs.Invalid("request body must be a JSON object", nil)
	}
	return nil
}
