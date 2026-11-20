package deadletters

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

// Handler serves /api/v1/admin/dlq.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Routes mounts the DLQ routes. They must sit behind the Supabase auth
// middleware (api.Deps.Routes).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/admin/dlq", h.list)
	r.Post("/admin/dlq/{seq}/replay", h.replay)
}

type deadLetterResponse struct {
	Seq       uint64    `json:"seq"`
	Subject   string    `json:"subject"`
	EventID   string    `json:"event_id"`
	Consumer  string    `json:"consumer"`
	Attempts  int       `json:"attempts"`
	Error     string    `json:"error"`
	FailedAt  time.Time `json:"failed_at"`
	StoredAt  time.Time `json:"stored_at"`
	SizeBytes int       `json:"size_bytes"`
}

type listCursor struct {
	After uint64 `json:"after"`
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
	res, err := h.svc.List(r.Context(), p, cur.After, page.Limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]deadLetterResponse, len(res.Items))
	for i, d := range res.Items {
		items[i] = deadLetterResponse{
			Seq: d.Seq, Subject: d.Subject, EventID: d.EventID, Consumer: d.Consumer, Attempts: d.Attempts,
			Error: d.Error, FailedAt: d.FailedAt, StoredAt: d.StoredAt, SizeBytes: d.Size,
		}
	}
	var next string
	if res.Next != 0 {
		next = httpx.EncodeCursor(listCursor{After: res.Next})
	}
	httpx.JSONPage(w, items, next)
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	seq, err := strconv.ParseUint(chi.URLParam(r, "seq"), 10, 64)
	if err != nil || seq == 0 {
		httpx.Error(w, r, errs.NotFound("dead letter not found"))
		return
	}
	if err := h.svc.Replay(r.Context(), p, seq); err != nil {
		httpx.Error(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "dead letter replayed", "seq", seq, "admin_id", p.ID)
	w.WriteHeader(http.StatusNoContent)
}

func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
	}
	return p, ok
}
