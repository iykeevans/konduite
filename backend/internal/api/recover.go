package api

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/iykeevans/konduite/backend/internal/platform/httpx"
)

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// net/http uses this sentinel to abort a response; it must keep propagating.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			slog.ErrorContext(r.Context(), "panic in handler",
				"method", r.Method, "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
			httpx.Fail(w, http.StatusInternalServerError, "internal", "internal server error", nil)
		}()
		next.ServeHTTP(w, r)
	})
}
