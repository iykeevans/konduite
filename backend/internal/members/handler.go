package members

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

// Handler serves workspace members and invitations under /api/v1.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the member and invitation routes behind the Supabase auth
// middleware. The invitation token travels only in the accept request body,
// never in a URL.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/workspaces/{workspaceID}/members", h.listMembers)
	r.Patch("/workspaces/{workspaceID}/members/{userID}", h.changeRole)
	r.Delete("/workspaces/{workspaceID}/members/{userID}", h.removeMember)
	r.Post("/workspaces/{workspaceID}/invitations", h.invite)
	r.Get("/workspaces/{workspaceID}/invitations", h.listInvitations)
	r.Delete("/workspaces/{workspaceID}/invitations/{invitationID}", h.revokeInvitation)
	r.Post("/invitations/accept", h.accept)
}

type memberResponse struct {
	UserID   uuid.UUID `json:"user_id"`
	Role     string    `json:"role"`
	Email    *string   `json:"email"`
	JoinedAt time.Time `json:"joined_at"`
}

func toMember(m Member) memberResponse {
	out := memberResponse{UserID: m.UserID, Role: string(m.Role), JoinedAt: m.JoinedAt}
	if m.Email != "" {
		out.Email = &m.Email
	}
	return out
}

type invitationResponse struct {
	ID          uuid.UUID `json:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	InvitedBy   uuid.UUID `json:"invited_by"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func toInvitation(inv Invitation) invitationResponse {
	return invitationResponse{
		ID: inv.ID, WorkspaceID: inv.WorkspaceID, Email: inv.Email, Role: string(inv.Role),
		InvitedBy: inv.InvitedBy, ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt,
	}
}

type issuedResponse struct {
	invitationResponse
	Token string `json:"token"`
}

type memberCursor struct {
	JoinedAt time.Time `json:"joined_at"`
	UserID   uuid.UUID `json:"user_id"`
}

type invitationCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	p, ws, ok := principalAndWorkspace(w, r)
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var cur memberCursor
	if err := page.DecodeCursor(&cur); err != nil {
		httpx.Error(w, r, badCursor())
		return
	}
	res, err := h.svc.ListMembers(r.Context(), p, ws, MemberCursor(cur), page.Limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]memberResponse, len(res.Items))
	for i, m := range res.Items {
		items[i] = toMember(m)
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(memberCursor(*res.Next))
	}
	httpx.JSONPage(w, items, next)
}

type changeRoleRequest struct {
	Role string `json:"role"`
}

func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request) {
	p, ws, ok := principalAndWorkspace(w, r)
	if !ok {
		return
	}
	userID, err := pathID(r, "userID", "member not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req changeRoleRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	m, err := h.svc.ChangeRole(r.Context(), p, ws, userID, req.Role)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMember(m))
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	p, ws, ok := principalAndWorkspace(w, r)
	if !ok {
		return
	}
	userID, err := pathID(r, "userID", "member not found")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.RemoveMember(r.Context(), p, ws, userID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type inviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request) {
	p, ws, ok := principalAndWorkspace(w, r)
	if !ok {
		return
	}
	var req inviteRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	inv, err := h.svc.Invite(r.Context(), p, ws, InviteInput(req))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusCreated, issuedResponse{invitationResponse: toInvitation(inv.Invitation), Token: inv.Token})
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) {
	p, ws, ok := principalAndWorkspace(w, r)
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var cur invitationCursor
	if err := page.DecodeCursor(&cur); err != nil {
		httpx.Error(w, r, badCursor())
		return
	}
	res, err := h.svc.ListInvitations(r.Context(), p, ws, InvitationCursor(cur), page.Limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]invitationResponse, len(res.Items))
	for i, inv := range res.Items {
		items[i] = toInvitation(inv)
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(invitationCursor(*res.Next))
	}
	httpx.JSONPage(w, items, next)
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	p, ws, ok := principalAndWorkspace(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "invitationID", "invitation not found or no longer valid")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.RevokeInvitation(r.Context(), p, ws, id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type acceptRequest struct {
	Token string `json:"token"`
}

type joinedResponse struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Role        string    `json:"role"`
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req acceptRequest
	if err := decode(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	j, err := h.svc.Accept(r.Context(), p, req.Token)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, joinedResponse{WorkspaceID: j.WorkspaceID, Role: string(j.Role)})
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
	}
	return p, ok
}

func principalAndWorkspace(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, ok := principal(w, r)
	if !ok {
		return auth.Principal{}, uuid.Nil, false
	}
	ws, err := pathID(r, "workspaceID", "workspace not found")
	if err != nil {
		httpx.Error(w, r, err)
		return auth.Principal{}, uuid.Nil, false
	}
	return p, ws, true
}

// A malformed id gets the same 404 as a missing or hidden one.
func pathID(r *http.Request, param, notFound string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		return uuid.Nil, errs.NotFound(notFound)
	}
	return id, nil
}

func badCursor() error {
	return errs.Invalid("invalid pagination parameters", map[string]string{"cursor": "is not a valid cursor"})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(dst); err != nil {
		return errs.Invalid("request body must be a JSON object", nil)
	}
	return nil
}
