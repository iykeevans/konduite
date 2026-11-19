package log

import (
	"net/http"
	"regexp"

	"github.com/google/uuid"
)

// Headers Middleware reads and sets.
const (
	RequestIDHeader     = "X-Request-ID"
	CorrelationIDHeader = "X-Correlation-ID"
)

// Incoming IDs are echoed in response headers and written to logs, so only
// short, plain tokens are trusted; anything else is replaced.
var safeID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Middleware gives every request a request ID and a correlation ID, puts
// both in the request context and sets them on the response. A safe incoming
// X-Request-ID is kept; the correlation ID comes from X-Correlation-ID or
// falls back to the request ID.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get(RequestIDHeader)
		if !safeID.MatchString(reqID) {
			reqID = uuid.NewString()
		}
		corrID := r.Header.Get(CorrelationIDHeader)
		if !safeID.MatchString(corrID) {
			corrID = reqID
		}
		w.Header().Set(RequestIDHeader, reqID)
		w.Header().Set(CorrelationIDHeader, corrID)
		ctx := WithCorrelationID(WithRequestID(r.Context(), reqID), corrID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
