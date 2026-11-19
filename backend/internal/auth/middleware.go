package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

// TokenVerifier turns a bearer token into a Principal. *Verifier implements it.
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (Principal, error)
}

// Middleware rejects requests without a valid bearer token and stores the
// caller's Principal in the request context for PrincipalFrom.
func Middleware(v TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearer(r.Header.Get("Authorization"))
			if !ok {
				unauthenticated(w, r)
				return
			}
			p, err := v.Verify(r.Context(), token)
			if errors.Is(err, ErrInvalidToken) {
				unauthenticated(w, r)
				return
			}
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

func bearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func unauthenticated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	httpx.Error(w, r, errs.Unauthenticated("missing or invalid access token"))
}
