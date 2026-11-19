package log_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iykeevans/konduite/backend/internal/platform/log"
)

func serve(t *testing.T, headers map[string]string) (rec *httptest.ResponseRecorder, reqID, corrID string) {
	t.Helper()
	h := log.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reqID = log.RequestID(r.Context())
		corrID = log.CorrelationID(r.Context())
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reqID, corrID
}

func TestMiddlewareAssignsIDs(t *testing.T) {
	tests := []struct {
		name      string
		headers   map[string]string
		wantReq   string
		wantCorr  string
		generated bool
		corrIsReq bool
	}{
		{name: "none incoming", generated: true, corrIsReq: true},
		{name: "safe request id", headers: map[string]string{"X-Request-ID": "abc-123_x.y:z"}, wantReq: "abc-123_x.y:z", corrIsReq: true},
		{name: "correlation id", headers: map[string]string{"X-Correlation-ID": "flow-9"}, generated: true, wantCorr: "flow-9"},
		{name: "both", headers: map[string]string{"X-Request-ID": "r1", "X-Correlation-ID": "c1"}, wantReq: "r1", wantCorr: "c1"},
		{name: "unsafe request id", headers: map[string]string{"X-Request-ID": "bad id"}, generated: true, corrIsReq: true},
		{name: "html in request id", headers: map[string]string{"X-Request-ID": "<script>"}, generated: true, corrIsReq: true},
		{name: "oversized request id", headers: map[string]string{"X-Request-ID": strings.Repeat("a", 129)}, generated: true, corrIsReq: true},
		{name: "unsafe correlation id", headers: map[string]string{"X-Request-ID": "r1", "X-Correlation-ID": "c 1"}, wantReq: "r1", corrIsReq: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, reqID, corrID := serve(t, tt.headers)

			if reqID == "" {
				t.Fatal("no request id in context")
			}
			if !tt.generated && reqID != tt.wantReq {
				t.Errorf("request id = %q, want %q", reqID, tt.wantReq)
			}
			if tt.generated && reqID == tt.headers["X-Request-ID"] {
				t.Errorf("request id = %q, want a generated one", reqID)
			}
			if tt.corrIsReq && corrID != reqID {
				t.Errorf("correlation id = %q, want request id %q", corrID, reqID)
			}
			if !tt.corrIsReq && corrID != tt.wantCorr {
				t.Errorf("correlation id = %q, want %q", corrID, tt.wantCorr)
			}
			if got := rec.Header().Get("X-Request-ID"); got != reqID {
				t.Errorf("X-Request-ID response header = %q, want %q", got, reqID)
			}
			if got := rec.Header().Get("X-Correlation-ID"); got != corrID {
				t.Errorf("X-Correlation-ID response header = %q, want %q", got, corrID)
			}
		})
	}
}

func TestMiddlewareGeneratesDistinctIDs(t *testing.T) {
	_, a, _ := serve(t, nil)
	_, b, _ := serve(t, nil)
	if a == b {
		t.Errorf("two requests got the same id %q", a)
	}
}
