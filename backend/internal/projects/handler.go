package projects

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

// Handler serves /api/v1/workspaces/{workspaceID}/projects and
// /api/v1/projects/{projectID}.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the project routes. They must sit behind the Supabase auth
// middleware (api.Deps.Routes) and are registered by full path, never with
// r.Route, so they can share the /workspaces/{workspaceID} prefix with the
// workspaces package.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/workspaces/{workspaceID}/projects", h.create)
	r.Get("/workspaces/{workspaceID}/projects", h.list)
	r.Get("/projects/{projectID}", h.get)
	r.Patch("/projects/{projectID}", h.update)
}

type projectResponse struct {
	ID            uuid.UUID `json:"id"`
	WorkspaceID   uuid.UUID `json:"workspace_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	RepositoryURL *string   `json:"repository_url"`
	CreatedBy     uuid.UUID `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toResponse(p Project) projectResponse {
	res := projectResponse{
		ID: p.ID, WorkspaceID: p.WorkspaceID, Name: p.Name, Description: p.Description,
		CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if p.RepositoryURL != "" {
		res.RepositoryURL = &p.RepositoryURL
	}
	return res
}

type createRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	RepositoryURL string `json:"repository_url"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	wsID, err := pathID(r, "workspaceID", "workspace not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req createRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	project, err := h.svc.Create(r.Context(), p, wsID, CreateInput(req))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+project.ID.String())
	httpx.JSON(w, http.StatusCreated, toResponse(project))
}

type listCursor struct {
	After string `json:"after"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	wsID, err := pathID(r, "workspaceID", "workspace not found")
	if err != nil {
		httpx.Error(w, r, err)
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
	res, err := h.svc.List(r.Context(), p, wsID, ListInput{After: cur.After, Limit: page.Limit})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]projectResponse, len(res.Items))
	for i, project := range res.Items {
		items[i] = toResponse(project)
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
	id, err := pathID(r, "projectID", "project not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	project, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(project))
}

type updateRequest struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	RepositoryURL *string `json:"repository_url"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "projectID", "project not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req updateRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	project, err := h.svc.Update(r.Context(), p, id, UpdateInput(req))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(project))
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
	}
	return p, ok
}

// A malformed id gets the same 404 as a resource the caller cannot see.
func pathID(r *http.Request, param, notFound string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		return uuid.Nil, errs.NotFound(notFound)
	}
	return id, nil
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		return errs.Invalid("request body must be a JSON object", nil)
	}
	return nil
}
