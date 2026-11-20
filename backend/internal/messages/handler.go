package messages

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/conversations"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

// Room for MaxContentLen characters even when every one is JSON-escaped.
const maxBodyBytes = 1 << 20

// IdempotencyKeyHeader names the request header that makes a post retryable.
const IdempotencyKeyHeader = "Idempotency-Key"

// Handler serves /api/v1/conversations/{id}/messages.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the message routes. They must sit behind the Supabase auth
// middleware (api.Deps.Routes).
func (h *Handler) Routes(r chi.Router) {
	r.Post("/conversations/{id}/messages", h.post)
	r.Get("/conversations/{id}/messages", h.list)
}

type postRequest struct {
	Content     string          `json:"content"`
	MessageType Type            `json:"message_type"`
	ParentID    *string         `json:"parent_id"`
	Metadata    json.RawMessage `json:"metadata"`
}

type senderResponse struct {
	Type conversations.ParticipantKind `json:"type"`
	ID   uuid.UUID                     `json:"id"`
}

type messageResponse struct {
	ID             uuid.UUID       `json:"id"`
	ConversationID uuid.UUID       `json:"conversation_id"`
	SequenceNumber int64           `json:"sequence_number"`
	Sender         senderResponse  `json:"sender"`
	IdempotencyKey string          `json:"idempotency_key"`
	ParentID       *uuid.UUID      `json:"parent_id"`
	MessageType    Type            `json:"message_type"`
	Content        string          `json:"content"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      time.Time       `json:"created_at"`
}

type cursor struct {
	AfterSeq int64 `json:"after_seq"`
}

func toResponse(m Message) messageResponse {
	return messageResponse{
		ID: m.ID, ConversationID: m.ConversationID, SequenceNumber: m.SequenceNumber,
		Sender:         senderResponse{Type: m.Sender.Kind, ID: m.Sender.ID},
		IdempotencyKey: m.IdempotencyKey, ParentID: m.ParentID, MessageType: m.Type,
		Content: m.Content, Metadata: m.Metadata, CreatedAt: m.CreatedAt,
	}
}

func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	p, id, ok := principalAndConversation(w, r)
	if !ok {
		return
	}
	var req postRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		httpx.Error(w, r, errs.Invalid("request body must be a JSON object", nil))
		return
	}
	in := PostInput{
		IdempotencyKey: r.Header.Get(IdempotencyKeyHeader),
		Type:           req.MessageType,
		Content:        req.Content,
	}
	if string(req.Metadata) != "null" {
		in.Metadata = req.Metadata
	}
	if req.ParentID != nil {
		parent, err := uuid.Parse(*req.ParentID)
		if err != nil {
			httpx.Error(w, r, errs.Invalid("invalid message", map[string]string{"parent_id": "must be a UUID"}))
			return
		}
		in.ParentID = &parent
	}
	m, created, err := h.svc.Post(r.Context(), p, id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, toResponse(m))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, id, ok := principalAndConversation(w, r)
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	in := ListInput{Limit: page.Limit}
	q := r.URL.Query()
	fields := map[string]string{}
	switch {
	case q.Has("after_seq") && page.Cursor != "":
		fields["after_seq"] = "cannot be combined with cursor"
	case q.Has("after_seq"):
		n, err := strconv.ParseInt(q.Get("after_seq"), 10, 64)
		if err != nil {
			fields["after_seq"] = "must be an integer"
		}
		in.AfterSeq = n
	case page.Cursor != "":
		var c cursor
		if err := page.DecodeCursor(&c); err != nil {
			fields["cursor"] = "is not a valid cursor"
		}
		in.AfterSeq = c.AfterSeq
	}
	if len(fields) > 0 {
		httpx.Error(w, r, errs.Invalid("invalid message query", fields))
		return
	}
	res, err := h.svc.List(r.Context(), p, id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]messageResponse, 0, len(res.Items))
	for _, m := range res.Items {
		items = append(items, toResponse(m))
	}
	var next string
	if res.Next != nil {
		next = httpx.EncodeCursor(cursor{AfterSeq: *res.Next})
	}
	httpx.JSONPage(w, items, next)
}

func principalAndConversation(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
		return auth.Principal{}, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, errs.NotFound("conversation not found"))
		return auth.Principal{}, uuid.Nil, false
	}
	return p, id, true
}
