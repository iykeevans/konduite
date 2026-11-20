package projectmembers

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

// Handler serves /api/v1/projects/{projectID}/members.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the project member routes behind the Supabase auth
// middleware, by full path so they can share /projects/{projectID} with the
// projects package.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/projects/{projectID}/members", h.list)
	r.Post("/projects/{projectID}/members", h.add)
	r.Patch("/projects/{projectID}/members/{userID}", h.changeRole)
	r.Delete("/projects/{projectID}/members/{userID}", h.remove)
}

type memberResponse struct {
	UserID        uuid.UUID `json:"user_id"`
	Role          string    `json:"role"`
	Email         *string   `json:"email"`
	WorkspaceRole string    `json:"workspace_role"`
	JoinedAt      time.Time `json:"joined_at"`
}

func toResponse(m Member) memberResponse {
	out := memberResponse{UserID: m.UserID, Role: string(m.Role), WorkspaceRole: string(m.WorkspaceRole), JoinedAt: m.JoinedAt}
	if m.Email != "" {
		out.Email = &m.Email
	}
	return out
}

type cursor struct {
	JoinedAt time.Time `json:"joined_at"`
	UserID   uuid.UUID `json:"user_id"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := principalAndProject(w, r)
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var cur cursor
	if err := page.DecodeCursor(&cur); err != nil {
		httpx.Error(w, r, errs.Invalid("invalid pagination parameters", map[string]string{"cursor": "is not a valid cursor"}))
		return
	}
	res, err := h.svc.List(r.Context(), p, projectID, Cursor(cur), page.Limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]memberResponse, len(res.Items))
	for i, m := range res.Items {
		items[i] = toResponse(m)
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(cursor(*res.Next))
	}
	httpx.JSONPage(w, items, next)
}

type addRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

func (h *Handler) add(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := principalAndProject(w, r)
	if !ok {
		return
	}
	var req addRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	var userID uuid.UUID
	if req.UserID != "" {
		id, err := uuid.Parse(req.UserID)
		if err != nil {
			httpx.Error(w, r, errs.Invalid("invalid project member", map[string]string{"user_id": "must be a user id"}))
			return
		}
		userID = id
	}
	m, err := h.svc.Add(r.Context(), p, projectID, userID, req.Role)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toResponse(m))
}

type changeRoleRequest struct {
	Role string `json:"role"`
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := principalAndProject(w, r)
	if !ok {
		return
	}
	userID, err := pathID(r, "userID", "project member not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req changeRoleRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	m, err := h.svc.ChangeRole(r.Context(), p, projectID, userID, req.Role)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(m))
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	p, projectID, ok := principalAndProject(w, r)
	if !ok {
		return
	}
	userID, err := pathID(r, "userID", "project member not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.Remove(r.Context(), p, projectID, userID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func principalAndProject(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
		return auth.Principal{}, uuid.Nil, false
	}
	projectID, err := pathID(r, "projectID", "project not found")
	if err != nil {
		httpx.Error(w, r, err)
		return auth.Principal{}, uuid.Nil, false
	}
	return p, projectID, true
}

// A malformed id gets the same 404 as a missing or hidden one.
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
