package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
)

type fakeVerifier map[string]auth.Principal

func (f fakeVerifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	if token == "jwks-down" {
		return auth.Principal{}, errors.New("auth: fetch JWKS: connection refused")
	}
	p, ok := f[token]
	if !ok {
		return auth.Principal{}, auth.ErrInvalidToken
	}
	return p, nil
}

func serve(t *testing.T, v auth.TokenVerifier, header string) (*httptest.ResponseRecorder, *auth.Principal) {
	t.Helper()
	var seen *auth.Principal
	h := auth.Middleware(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := auth.PrincipalFrom(r.Context()); ok {
			seen = &p
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/workspaces", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen
}

func TestMiddlewarePutsTheVerifiedPrincipalInTheContext(t *testing.T) {
	want := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	for _, header := range []string{"Bearer good", "bearer good"} {
		rec, seen := serve(t, fakeVerifier{"good": want}, header)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%q: status = %d, want 204", header, rec.Code)
		}
		if seen == nil || *seen != want {
			t.Errorf("%q: principal = %v, want %v", header, seen, want)
		}
	}
}

func TestMiddlewareRejectsMissingOrInvalidCredentials(t *testing.T) {
	for name, header := range map[string]string{
		"no header":     "",
		"basic auth":    "Basic dXNlcjpwYXNz",
		"empty bearer":  "Bearer ",
		"invalid token": "Bearer forged",
	} {
		t.Run(name, func(t *testing.T) {
			rec, seen := serve(t, fakeVerifier{}, header)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if seen != nil {
				t.Error("handler ran without a principal")
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rec.Body, err)
			}
			if body.Error.Code != "unauthenticated" || strings.Contains(body.Error.Message, "auth:") {
				t.Errorf("error = %+v, want code unauthenticated and no verifier detail", body.Error)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}
}

func TestMiddlewareDoesNotTreatAVerifierOutageAsBadCredentials(t *testing.T) {
	rec, seen := serve(t, fakeVerifier{}, "Bearer jwks-down")
	if rec.Code != http.StatusInternalServerError || seen != nil {
		t.Errorf("status = %d, want 500 so clients keep their session", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("body leaks the cause: %s", rec.Body)
	}
}
