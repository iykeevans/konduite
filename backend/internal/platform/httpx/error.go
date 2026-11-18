package httpx

import (
	"log/slog"
	"net/http"

	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

var statusByKind = map[errs.Kind]int{
	errs.KindNotFound:        http.StatusNotFound,
	errs.KindInvalid:         http.StatusUnprocessableEntity,
	errs.KindUnauthenticated: http.StatusUnauthorized,
	errs.KindForbidden:       http.StatusForbidden,
	errs.KindConflict:        http.StatusConflict,
}

// Error writes err as an error envelope. Domain errors (package errs) keep
// their message; anything else becomes a generic 500 and is logged.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := errs.As(err)
	status, known := http.StatusInternalServerError, false
	if ok {
		status, known = statusByKind[e.Kind]
	}
	if !known {
		slog.ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "error", err)
		Fail(w, http.StatusInternalServerError, "internal", "internal server error", nil)
		return
	}
	var details any
	if len(e.Fields) > 0 {
		details = map[string]any{"fields": e.Fields}
	}
	Fail(w, status, string(e.Kind), e.Message, details)
}
