package httpx_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

func TestErrorMapsDomainErrorsToStatusAndEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
		wantDetails any
	}{
		{"not found", errs.NotFound("workspace not found"), 404, "not_found", "workspace not found", nil},
		{"invalid with fields", errs.Invalid("invalid request", map[string]string{"slug": "already taken"}),
			422, "invalid", "invalid request", map[string]any{"fields": map[string]any{"slug": "already taken"}}},
		{"unauthenticated", errs.Unauthenticated("token expired"), 401, "unauthenticated", "token expired", nil},
		{"forbidden", errs.Forbidden("not a workspace admin"), 403, "forbidden", "not a workspace admin", nil},
		{"conflict", errs.Conflict("slug already exists"), 409, "conflict", "slug already exists", nil},
		{"wrapped domain error", fmt.Errorf("load project: %w", errs.NotFound("project not found")),
			404, "not_found", "project not found", nil},
		{"unknown error is hidden", errors.New("pq: relation \"secrets\" does not exist"),
			500, "internal", "internal server error", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			httpx.Error(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil), tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details any    `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("not JSON: %v", err)
			}
			if body.Error.Code != tt.wantCode || body.Error.Message != tt.wantMessage {
				t.Errorf("error = %s/%q, want %s/%q", body.Error.Code, body.Error.Message, tt.wantCode, tt.wantMessage)
			}
			if !reflect.DeepEqual(body.Error.Details, tt.wantDetails) {
				t.Errorf("details = %#v, want %#v", body.Error.Details, tt.wantDetails)
			}
			if strings.Contains(rec.Body.String(), "secrets") {
				t.Errorf("response leaks internal error: %s", rec.Body.String())
			}
		})
	}
}
