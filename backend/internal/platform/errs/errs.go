// Package errs defines the domain error kinds that handlers return.
// Only httpx knows how a kind maps to an HTTP status.
package errs

import "errors"

// Kind classifies a domain error.
type Kind string

// Error kinds.
const (
	KindNotFound        Kind = "not_found"
	KindInvalid         Kind = "invalid"
	KindUnauthenticated Kind = "unauthenticated"
	KindForbidden       Kind = "forbidden"
	KindConflict        Kind = "conflict"
)

// Error is a domain error whose message is safe to show to API callers.
type Error struct {
	Kind    Kind
	Message string
	// Fields maps request field names to validation problems (KindInvalid only).
	Fields map[string]string
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Message }

// As returns the *Error in err's chain, if any.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// NotFound reports a missing resource.
func NotFound(msg string) error { return &Error{Kind: KindNotFound, Message: msg} }

// Invalid reports a validation failure, optionally per field.
func Invalid(msg string, fields map[string]string) error {
	return &Error{Kind: KindInvalid, Message: msg, Fields: fields}
}

// Unauthenticated reports missing or bad credentials.
func Unauthenticated(msg string) error { return &Error{Kind: KindUnauthenticated, Message: msg} }

// Forbidden reports an authenticated caller without permission.
func Forbidden(msg string) error { return &Error{Kind: KindForbidden, Message: msg} }

// Conflict reports a clash with existing state.
func Conflict(msg string) error { return &Error{Kind: KindConflict, Message: msg} }
