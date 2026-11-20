package runtimes

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
)

const maxBodyBytes = 16 << 10

// Handler serves the runtime registration and token endpoints.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the user-facing routes. They must sit behind the Supabase
// auth middleware (api.Deps.Routes).
func (h *Handler) Routes(r chi.Router) {
	r.Post("/runtimes/register", h.register)
	r.Get("/runtimes", h.list)
	r.Get("/runtimes/{runtimeID}", h.get)
}

// TokenRoutes mounts the routes a runtime calls as itself: token exchange
// and revocation (refresh credential in the body) and self (runtime access
// token). They must not sit behind the Supabase auth middleware
// (api.Deps.PublicRoutes).
func (h *Handler) TokenRoutes(r chi.Router) {
	r.Post("/v1/runtime/token", h.token)
	r.Post("/v1/runtime/revoke", h.revoke)
	r.Get("/v1/runtime/self", h.self)
}

type runtimeResponse struct {
	ID                   uuid.UUID  `json:"id"`
	WorkspaceID          uuid.UUID  `json:"workspace_id"`
	OwnerID              uuid.UUID  `json:"owner_id"`
	Name                 string     `json:"name"`
	Revoked              bool       `json:"revoked"`
	CredentialLastUsedAt *time.Time `json:"credential_last_used_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func toResponse(rt Runtime) runtimeResponse {
	return runtimeResponse{
		ID: rt.ID, WorkspaceID: rt.WorkspaceID, OwnerID: rt.OwnerID, Name: rt.Name, Revoked: rt.Revoked,
		CredentialLastUsedAt: rt.CredentialLastUsedAt, CreatedAt: rt.CreatedAt, UpdatedAt: rt.UpdatedAt,
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "runtimeID"))
	if err != nil {
		httpx.Error(w, r, errs.NotFound("runtime not found"))
		return
	}
	rt, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(rt))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	fields := map[string]string{}
	in := ListInput{Limit: page.Limit}
	if raw := r.URL.Query().Get("workspace_id"); raw != "" {
		if in.WorkspaceID, err = uuid.Parse(raw); err != nil {
			fields["workspace_id"] = "must be a UUID"
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
		httpx.Error(w, r, errs.Invalid("invalid runtime query", fields))
		return
	}
	res, err := h.svc.List(r.Context(), p, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]runtimeResponse, 0, len(res.Items))
	for _, rt := range res.Items {
		items = append(items, toResponse(rt))
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(res.Next)
	}
	httpx.JSONPage(w, items, next)
}

func (h *Handler) self(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		httpx.Error(w, r, errs.Unauthenticated("missing runtime access token"))
		return
	}
	rt, err := h.svc.Self(r.Context(), token)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, toResponse(rt))
}

type registerRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
}

type registrationResponse struct {
	RuntimeID         uuid.UUID `json:"runtime_id"`
	WorkspaceID       uuid.UUID `json:"workspace_id"`
	OwnerID           uuid.UUID `json:"owner_id"`
	Name              string    `json:"name"`
	RefreshCredential string    `json:"refresh_credential"`
	CreatedAt         time.Time `json:"created_at"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
		return
	}
	var req registerRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	workspaceID, err := uuid.Parse(req.WorkspaceID)
	if err != nil && req.WorkspaceID != "" {
		httpx.Error(w, r, errs.Invalid("invalid runtime registration", map[string]string{"workspace_id": "must be a UUID"}))
		return
	}
	reg, err := h.svc.Register(r.Context(), p, RegisterInput{WorkspaceID: workspaceID, Name: req.Name})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusCreated, registrationResponse(reg))
}

type tokenRequest struct {
	RefreshCredential string `json:"refresh_credential"`
}

type tokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	cred, err := refreshCredential(w, r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	tok, err := h.svc.Exchange(r.Context(), cred)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, tokenResponse{
		AccessToken: tok.Token,
		TokenType:   "Bearer",
		ExpiresIn:   int(TokenTTL / time.Second),
		ExpiresAt:   tok.ExpiresAt,
	})
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	cred, err := refreshCredential(w, r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.Revoke(r.Context(), cred); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func refreshCredential(w http.ResponseWriter, r *http.Request) (string, error) {
	var req tokenRequest
	if err := decode(w, r, &req); err != nil {
		return "", err
	}
	if req.RefreshCredential == "" {
		return "", errs.Invalid("refresh_credential is required in the request body",
			map[string]string{"refresh_credential": "required"})
	}
	return req.RefreshCredential, nil
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		return errs.Invalid("request body must be a JSON object", nil)
	}
	return nil
}
