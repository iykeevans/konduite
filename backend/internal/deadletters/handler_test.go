package deadletters_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/deadletters"
	"github.com/iykeevans/konduite/backend/internal/platform/nats"
)

func serve(t *testing.T, store *fakeStore, p *auth.Principal, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	deadletters.NewHandler(newService(store)).Routes(r)
	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

type listBody struct {
	Data []map[string]any `json:"data"`
	Meta struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"meta"`
}

func TestListHandler(t *testing.T) {
	failed := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	store := &fakeStore{entries: []nats.DeadLetter{
		{Seq: 1, Subject: "agenthub.message.created", EventID: "evt-1", Consumer: "deliveries", Attempts: 5, Error: "boom", FailedAt: failed, StoredAt: failed, Size: 10},
		{Seq: 2, Subject: "agenthub.message.created"},
	}}

	rec := serve(t, store, &adminUser, http.MethodGet, "/admin/dlq?limit=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body listBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || !body.Meta.HasMore || body.Meta.NextCursor == "" {
		t.Fatalf("body = %+v", body)
	}
	item := body.Data[0]
	for k, v := range map[string]any{
		"seq": 1.0, "subject": "agenthub.message.created", "event_id": "evt-1", "consumer": "deliveries",
		"attempts": 5.0, "error": "boom", "failed_at": "2026-11-20T09:00:00Z", "size_bytes": 10.0,
	} {
		if item[k] != v {
			t.Errorf("%s = %v, want %v", k, item[k], v)
		}
	}
	if _, ok := item["data"]; ok {
		t.Error("list exposes the payload")
	}

	rec = serve(t, store, &adminUser, http.MethodGet, "/admin/dlq?limit=1&cursor="+body.Meta.NextCursor)
	body = listBody{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Data) != 1 || body.Data[0]["seq"] != 2.0 || body.Meta.HasMore {
		t.Errorf("second page = %+v", body)
	}
}

func TestHandlerStatusCodes(t *testing.T) {
	other := auth.Principal{Kind: auth.KindUser, ID: [16]byte{1}}
	tests := []struct {
		name   string
		p      *auth.Principal
		method string
		target string
		want   int
	}{
		{"list as admin", &adminUser, http.MethodGet, "/admin/dlq", http.StatusOK},
		{"list as non-admin is hidden", &other, http.MethodGet, "/admin/dlq", http.StatusNotFound},
		{"list without a principal", nil, http.MethodGet, "/admin/dlq", http.StatusUnauthorized},
		{"list with a bad limit", &adminUser, http.MethodGet, "/admin/dlq?limit=0", http.StatusUnprocessableEntity},
		{"list with a bad cursor", &adminUser, http.MethodGet, "/admin/dlq?cursor=%21", http.StatusUnprocessableEntity},
		{"replay", &adminUser, http.MethodPost, "/admin/dlq/1/replay", http.StatusNoContent},
		{"replay a missing entry", &adminUser, http.MethodPost, "/admin/dlq/99/replay", http.StatusNotFound},
		{"replay a malformed seq", &adminUser, http.MethodPost, "/admin/dlq/abc/replay", http.StatusNotFound},
		{"replay as non-admin is hidden", &other, http.MethodPost, "/admin/dlq/1/replay", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, &fakeStore{entries: entries(1)}, tt.p, tt.method, tt.target)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
		})
	}
}
