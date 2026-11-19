package conversations

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

// Handler serves /api/v1/conversations.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the conversation routes. They must sit behind the Supabase
// auth middleware (api.Deps.Routes).
func (h *Handler) Routes(r chi.Router) {
	r.Post("/conversations", h.create)
	r.Get("/conversations", h.list)
	r.Get("/conversations/{id}", h.get)
}

type createRequest struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   *string `json:"project_id"`
	Title       string  `json:"title"`
}

type participantResponse struct {
	Type     ParticipantKind `json:"type"`
	ID       uuid.UUID       `json:"id"`
	JoinedAt time.Time       `json:"joined_at"`
}

type conversationResponse struct {
	ID           uuid.UUID             `json:"id"`
	WorkspaceID  uuid.UUID             `json:"workspace_id"`
	ProjectID    *uuid.UUID            `json:"project_id"`
	Title        string                `json:"title"`
	CreatedBy    uuid.UUID             `json:"created_by"`
	LastSeq      int64                 `json:"last_seq"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
	Participants []participantResponse `json:"participants,omitempty"`
}

func toResponse(c Conversation) conversationResponse {
	resp := conversationResponse{
		ID: c.ID, WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, Title: c.Title,
		CreatedBy: c.CreatedBy, LastSeq: c.LastSeq, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	for _, p := range c.Participants {
		resp.Participants = append(resp.Participants, participantResponse{Type: p.Kind, ID: p.ID, JoinedAt: p.JoinedAt})
	}
	return resp
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		httpx.Error(w, r, errs.Invalid("request body must be a JSON object", nil))
		return
	}
	fields := map[string]string{}
	workspaceID := parseUUID(req.WorkspaceID, "workspace_id", fields)
	var projectID *uuid.UUID
	if req.ProjectID != nil {
		if id := parseUUID(*req.ProjectID, "project_id", fields); id != uuid.Nil {
			projectID = &id
		}
	}
	if len(fields) > 0 {
		httpx.Error(w, r, errs.Invalid("invalid conversation", fields))
		return
	}
	c, err := h.svc.Create(r.Context(), p, CreateInput{WorkspaceID: workspaceID, ProjectID: projectID, Title: req.Title})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toResponse(c))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, errs.NotFound("conversation not found"))
		return
	}
	c, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(c))
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
	q := r.URL.Query()
	fields := map[string]string{}
	in := ListInput{WorkspaceID: parseUUID(q.Get("workspace_id"), "workspace_id", fields), Limit: page.Limit}
	if q.Has("project_id") {
		if id := parseUUID(q.Get("project_id"), "project_id", fields); id != uuid.Nil {
			in.ProjectID = &id
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
		httpx.Error(w, r, errs.Invalid("invalid conversation query", fields))
		return
	}
	res, err := h.svc.List(r.Context(), p, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]conversationResponse, 0, len(res.Items))
	for _, c := range res.Items {
		items = append(items, toResponse(c))
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(res.Next)
	}
	httpx.JSONPage(w, items, next)
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
	}
	return p, ok
}

// parseUUID leaves an empty value as uuid.Nil so the service reports it as
// required, and records a malformed one in fields.
func parseUUID(s, field string, fields map[string]string) uuid.UUID {
	if s == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		fields[field] = "must be a UUID"
	}
	return id
}
