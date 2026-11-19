package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// ErrInvalidToken means the token itself was rejected. Any other error from
// Verify is an infrastructure failure, such as the JWKS being unreachable.
var ErrInvalidToken = errors.New("auth: invalid token")

// VerifierOptions configure a Verifier.
type VerifierOptions struct {
	JWKSURL  string
	Issuer   string
	Audience string
	// MinRefresh is the shortest gap between JWKS fetches caused by a token
	// with an unknown kid, so random kids cannot make us hammer Supabase.
	MinRefresh time.Duration
	// MaxAge is how long fetched keys are trusted before a refetch. Default 10m.
	MaxAge     time.Duration
	HTTPClient *http.Client
}

// SupabaseOptions returns the options for a Supabase project, local or hosted.
func SupabaseOptions(projectURL string) VerifierOptions {
	base := strings.TrimRight(projectURL, "/") + "/auth/v1"
	return VerifierOptions{
		JWKSURL:    base + "/.well-known/jwks.json",
		Issuer:     base,
		Audience:   "authenticated",
		MinRefresh: time.Minute,
	}
}

// Verifier checks Supabase access tokens against the project's JWKS.
type Verifier struct {
	opts VerifierOptions

	mu          sync.Mutex
	keys        jwk.Set
	fetchedAt   time.Time
	lastMiss    time.Time
	lastFailure time.Time
}

// NewVerifier returns a Verifier. Keys are fetched lazily on first use.
func NewVerifier(opts VerifierOptions) *Verifier {
	if opts.MaxAge <= 0 {
		opts.MaxAge = 10 * time.Minute
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &Verifier{opts: opts}
}

// HMAC algorithms are excluded on purpose: with a public JWKS, accepting them
// would let anyone sign a token using a public key as the shared secret.
var allowedAlgs = map[string]bool{"ES256": true, "RS256": true}

// Verify returns the principal for a valid token, or an error wrapping
// ErrInvalidToken when the token is malformed, unsigned, expired or not ours.
func (v *Verifier) Verify(ctx context.Context, token string) (Principal, error) {
	msg, err := jws.ParseString(token)
	if err != nil {
		return Principal{}, invalid(err)
	}
	sigs := msg.Signatures()
	if len(sigs) != 1 {
		return Principal{}, invalid(errors.New("want exactly one signature"))
	}
	h := sigs[0].ProtectedHeaders()
	alg, ok := h.Algorithm()
	if !ok || !allowedAlgs[alg.String()] {
		return Principal{}, invalid(fmt.Errorf("algorithm %v not allowed", alg))
	}
	kid, ok := h.KeyID()
	if !ok || kid == "" {
		return Principal{}, invalid(errors.New("missing kid"))
	}

	keys, err := v.keySet(ctx, kid)
	if err != nil {
		return Principal{}, err
	}
	tok, err := jwt.ParseString(token,
		jwt.WithKeySet(keys),
		jwt.WithIssuer(v.opts.Issuer),
		jwt.WithAudience(v.opts.Audience),
		jwt.WithRequiredClaim("exp"),
		jwt.WithRequiredClaim("sub"),
	)
	if err != nil {
		return Principal{}, invalid(err)
	}
	sub, _ := tok.Subject()
	id, err := uuid.Parse(sub)
	if err != nil || id == uuid.Nil {
		return Principal{}, invalid(errors.New("subject is not a user id"))
	}
	return Principal{Kind: KindUser, ID: id}, nil
}

func (v *Verifier) keySet(ctx context.Context, kid string) (jwk.Set, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := time.Now()
	if v.keys != nil && now.Sub(v.fetchedAt) < v.opts.MaxAge {
		if _, ok := v.keys.LookupKeyID(kid); ok || now.Sub(v.lastMiss) < v.opts.MinRefresh {
			return v.keys, nil
		}
		v.lastMiss = now
	} else if v.keys != nil && now.Sub(v.lastFailure) < v.opts.MinRefresh {
		return v.keys, nil
	}

	set, err := jwk.Fetch(ctx, v.opts.JWKSURL, jwk.WithHTTPClient(v.opts.HTTPClient))
	if err != nil {
		v.lastFailure = now
		if v.keys != nil {
			// Keep serving the keys we have through a short Supabase outage.
			return v.keys, nil
		}
		return nil, fmt.Errorf("auth: fetch JWKS: %w", err)
	}
	v.keys, v.fetchedAt = set, now
	return set, nil
}

func invalid(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidToken, err)
}
