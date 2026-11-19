package runtimes

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
}

// TokenRoutes mounts the token exchange, which authenticates with a refresh
// credential instead of a user token, so it must not sit behind the Supabase
// auth middleware (api.Deps.PublicRoutes).
func (h *Handler) TokenRoutes(r chi.Router) {
	r.Post("/v1/runtime/token", h.token)
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
	var req tokenRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.RefreshCredential == "" {
		httpx.Error(w, r, errs.Invalid("refresh_credential is required in the request body",
			map[string]string{"refresh_credential": "required"}))
		return
	}
	tok, err := h.svc.Exchange(r.Context(), req.RefreshCredential)
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

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		return errs.Invalid("request body must be a JSON object", nil)
	}
	return nil
}
