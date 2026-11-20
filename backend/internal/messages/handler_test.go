package messages_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/messages"
)

type response struct {
	Data json.RawMessage `json:"data"`
	Meta *struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"meta"`
	Error struct {
		Code    string `json:"code"`
		Details struct {
			Fields map[string]string `json:"fields"`
		} `json:"details"`
	} `json:"error"`
}

type messageJSON struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	SequenceNumber int64  `json:"sequence_number"`
	Sender         struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"sender"`
	IdempotencyKey string          `json:"idempotency_key"`
	ParentID       *string         `json:"parent_id"`
	MessageType    string          `json:"message_type"`
	Content        string          `json:"content"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      string          `json:"created_at"`
}

func router(f fixture, user *auth.Principal) http.Handler {
	v1 := chi.NewRouter()
	messages.NewHandler(f.svc).Routes(v1)
	r := chi.NewRouter()
	r.Mount("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user != nil {
			req = req.WithContext(auth.WithPrincipal(req.Context(), *user))
		}
		v1.ServeHTTP(w, req)
	}))
	return r
}

func do(t *testing.T, h http.Handler, method, target, key, body string) (*httptest.ResponseRecorder, response) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	h.ServeHTTP(rec, req)
	var resp response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return rec, resp
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPostMessageReturns201ThenTheOriginalWith200OnARetry(t *testing.T) {
	f := newFixture(t)
	h := router(f, &f.contributor)
	target := "/api/v1/conversations/" + f.scoped.String() + "/messages"
	impostor := uuid.NewString()

	rec, resp := do(t, h, http.MethodPost, target, "req-1",
		`{"content":"ship it","message_type":"task","metadata":{"a":1},"sender":{"type":"user","id":"`+impostor+`"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	m := decode[messageJSON](t, resp.Data)
	if m.Sender.Type != "user" || m.Sender.ID != f.contributor.ID.String() {
		t.Errorf("sender = %+v, want the caller, never the body", m.Sender)
	}
	if m.ConversationID != f.scoped.String() || m.SequenceNumber != 1 || m.IdempotencyKey != "req-1" ||
		m.MessageType != "task" || m.Content != "ship it" || string(m.Metadata) != `{"a":1}` || m.CreatedAt == "" {
		t.Errorf("message = %+v", m)
	}

	rec, resp = do(t, h, http.MethodPost, target, "req-1", `{"content":"changed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if again := decode[messageJSON](t, resp.Data); again.ID != m.ID || again.Content != "ship it" {
		t.Errorf("retry = %+v, want the original", again)
	}
}

func TestPostMessageFailures(t *testing.T) {
	f := newFixture(t)
	target := "/api/v1/conversations/" + f.open.String() + "/messages"
	tests := []struct {
		name   string
		user   *auth.Principal
		target string
		key    string
		body   string
		status int
		field  string
	}{
		{"no principal", nil, target, "", `{"content":"hi"}`, http.StatusUnauthorized, ""},
		{"malformed conversation id", &f.teammate, "/api/v1/conversations/nope/messages", "", `{"content":"hi"}`, http.StatusNotFound, ""},
		{"hidden conversation", &f.stranger, target, "", `{"content":"hi"}`, http.StatusNotFound, ""},
		{"project viewer", &f.viewer, "/api/v1/conversations/" + f.scoped.String() + "/messages", "", `{"content":"hi"}`, http.StatusForbidden, ""},
		{"not JSON", &f.teammate, target, "", `hi`, http.StatusUnprocessableEntity, ""},
		{"empty content", &f.teammate, target, "", `{"content":""}`, http.StatusUnprocessableEntity, "content"},
		{"bad parent id", &f.teammate, target, "", `{"content":"hi","parent_id":"x"}`, http.StatusUnprocessableEntity, "parent_id"},
		{"key too long", &f.teammate, target, strings.Repeat("k", 256), `{"content":"hi"}`, http.StatusUnprocessableEntity, "idempotency_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := do(t, router(f, tt.user), http.MethodPost, tt.target, tt.key, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			if tt.field != "" && resp.Error.Details.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want %s", resp.Error.Details.Fields, tt.field)
			}
		})
	}
}

func TestPostMessageAnswers500WhenTheWriteFails(t *testing.T) {
	f := newFixture(t)
	f.repo.failWith = errors.New("commit failed")
	rec, resp := do(t, router(f, &f.teammate), http.MethodPost, "/api/v1/conversations/"+f.open.String()+"/messages", "", `{"content":"hi"}`)
	if rec.Code != http.StatusInternalServerError || resp.Data != nil {
		t.Errorf("status = %d data %s, want 500 and no message", rec.Code, resp.Data)
	}
}

func TestListMessages(t *testing.T) {
	f := newFixture(t)
	h := router(f, &f.teammate)
	base := "/api/v1/conversations/" + f.open.String() + "/messages"
	for _, key := range []string{"a", "b", "c"} {
		if rec, _ := do(t, h, http.MethodPost, base, key, `{"content":"hi"}`); rec.Code != http.StatusCreated {
			t.Fatalf("post status = %d", rec.Code)
		}
	}
	seqs := func(resp response) []int64 {
		var out []int64
		for _, m := range decode[[]messageJSON](t, resp.Data) {
			out = append(out, m.SequenceNumber)
		}
		return out
	}

	rec, resp := do(t, h, http.MethodGet, base+"?limit=2", "", "")
	if rec.Code != http.StatusOK || resp.Meta == nil || !resp.Meta.HasMore || resp.Meta.NextCursor == "" {
		t.Fatalf("first page: status %d meta %+v", rec.Code, resp.Meta)
	}
	if got := seqs(resp); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("first page seqs = %v, want [1 2]", got)
	}
	_, resp = do(t, h, http.MethodGet, base+"?limit=2&cursor="+resp.Meta.NextCursor, "", "")
	if got := seqs(resp); len(got) != 1 || got[0] != 3 || resp.Meta.HasMore {
		t.Errorf("cursor page seqs = %v has_more %v, want [3] and no more", got, resp.Meta.HasMore)
	}
	_, resp = do(t, h, http.MethodGet, base+"?after_seq=1", "", "")
	if got := seqs(resp); len(got) != 2 || got[0] != 2 {
		t.Errorf("after_seq=1 seqs = %v, want [2 3]", got)
	}

	for _, q := range []string{"?after_seq=x", "?after_seq=-1", "?limit=0", "?after_seq=1&cursor=eyJhZnRlcl9zZXEiOjF9"} {
		if rec, _ := do(t, h, http.MethodGet, base+q, "", ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("GET %s status = %d, want 422", q, rec.Code)
		}
	}
	if rec, _ := do(t, router(f, &f.stranger), http.MethodGet, base, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("stranger status = %d, want 404", rec.Code)
	}
}
