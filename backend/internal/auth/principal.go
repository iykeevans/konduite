// Package auth authenticates requests and carries the caller's identity.
package auth

import (
	"context"

	"github.com/google/uuid"
)

// Kind says what sort of caller a Principal is.
type Kind string

// KindUser is a person signed in through Supabase Auth.
const KindUser Kind = "user"

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind Kind
	ID   uuid.UUID
	// Email is the lowercased email claim of a user's Supabase token, or
	// empty when it has none. It proves the user controls the address only
	// when the Supabase project requires email confirmation (local dev
	// doesn't).
	Email string
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal Middleware stored in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
