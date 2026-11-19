package auth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/iykeevans/konduite/backend/internal/auth"
)

const (
	issuer   = "http://127.0.0.1:54321/auth/v1"
	audience = "authenticated"
)

// jwksServer serves the public half of the keys it holds and counts fetches.
type jwksServer struct {
	*httptest.Server
	mu      sync.Mutex
	keys    []jwk.Key
	fetches atomic.Int32
}

func newJWKSServer(t *testing.T, keys ...jwk.Key) *jwksServer {
	t.Helper()
	s := &jwksServer{keys: keys}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		set := jwk.NewSet()
		for _, k := range s.keys {
			pub, err := k.PublicKey()
			if err != nil {
				t.Error(err)
				return
			}
			_ = set.AddKey(pub)
		}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) publish(k jwk.Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, k)
}

func ecKey(t *testing.T, kid string) jwk.Key {
	t.Helper()
	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := jwk.Import(raw)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.Set(jwk.KeyIDKey, kid)
	_ = k.Set(jwk.AlgorithmKey, jwa.ES256())
	return k
}

func rsaKey(t *testing.T, kid string) jwk.Key {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k, err := jwk.Import(raw)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.Set(jwk.KeyIDKey, kid)
	_ = k.Set(jwk.AlgorithmKey, jwa.RS256())
	return k
}

type claims struct {
	sub, iss, aud string
	exp, nbf      time.Time
}

func validClaims(sub uuid.UUID) claims {
	return claims{sub: sub.String(), iss: issuer, aud: audience, exp: time.Now().Add(time.Hour)}
}

func sign(t *testing.T, key jwk.Key, alg jwa.SignatureAlgorithm, c claims) string {
	t.Helper()
	b := jwt.NewBuilder().Subject(c.sub).Issuer(c.iss).Audience([]string{c.aud}).Expiration(c.exp).IssuedAt(time.Now())
	if !c.nbf.IsZero() {
		b = b.NotBefore(c.nbf)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(alg, key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func newVerifier(t *testing.T, s *jwksServer, minRefresh time.Duration) *auth.Verifier {
	t.Helper()
	return auth.NewVerifier(auth.VerifierOptions{
		JWKSURL: s.URL, Issuer: issuer, Audience: audience, MinRefresh: minRefresh,
	})
}

func TestVerifyAcceptsAValidSupabaseToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  jwk.Key
		alg  jwa.SignatureAlgorithm
	}{
		{"ES256", ecKey(t, "ec-1"), jwa.ES256()},
		{"RS256", rsaKey(t, "rsa-1"), jwa.RS256()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifier(t, newJWKSServer(t, tc.key), time.Minute)
			user := uuid.New()
			p, err := v.Verify(t.Context(), sign(t, tc.key, tc.alg, validClaims(user)))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if p.Kind != auth.KindUser || p.ID != user {
				t.Errorf("principal = %+v, want user %s", p, user)
			}
		})
	}
}

func TestVerifyRejectsBadTokens(t *testing.T) {
	trusted := ecKey(t, "ec-1")
	srv := newJWKSServer(t, trusted)
	user := uuid.New()
	mutate := func(f func(*claims)) claims {
		c := validClaims(user)
		f(&c)
		return c
	}

	hmac, err := jwk.Import([]byte("a-shared-secret-of-at-least-32-bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	_ = hmac.Set(jwk.KeyIDKey, "ec-1")

	cases := map[string]string{
		"expired":          sign(t, trusted, jwa.ES256(), mutate(func(c *claims) { c.exp = time.Now().Add(-time.Minute) })),
		"not yet valid":    sign(t, trusted, jwa.ES256(), mutate(func(c *claims) { c.nbf = time.Now().Add(time.Hour) })),
		"wrong issuer":     sign(t, trusted, jwa.ES256(), mutate(func(c *claims) { c.iss = "https://evil.example/auth/v1" })),
		"wrong audience":   sign(t, trusted, jwa.ES256(), mutate(func(c *claims) { c.aud = "anon" })),
		"subject not uuid": sign(t, trusted, jwa.ES256(), mutate(func(c *claims) { c.sub = "service_role" })),
		"untrusted key":    sign(t, ecKey(t, "ec-1"), jwa.ES256(), validClaims(user)),
		"HS256 with kid":   sign(t, hmac, jwa.HS256(), validClaims(user)),
		"alg none":         unsignedToken(t, user),
		"garbage":          "not.a.jwt",
		"empty":            "",
		"tampered payload": tamper(t, sign(t, trusted, jwa.ES256(), validClaims(user))),
	}
	v := newVerifier(t, srv, time.Minute)
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(t.Context(), token)
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("Verify = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func unsignedToken(t *testing.T, user uuid.UUID) string {
	t.Helper()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return b64(b)
	}
	return enc(map[string]string{"alg": "none", "typ": "JWT", "kid": "ec-1"}) + "." +
		enc(map[string]any{"sub": user.String(), "iss": issuer, "aud": audience, "exp": time.Now().Add(time.Hour).Unix()}) + "."
}

func TestVerifyRefetchesKeysForAnUnknownKidAtMostOncePerInterval(t *testing.T) {
	old := ecKey(t, "old")
	srv := newJWKSServer(t, old)
	v := newVerifier(t, srv, time.Hour)
	user := uuid.New()

	if _, err := v.Verify(t.Context(), sign(t, old, jwa.ES256(), validClaims(user))); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if got := srv.fetches.Load(); got != 1 {
		t.Fatalf("fetches after first verify = %d, want 1", got)
	}

	// An unknown kid triggers one refetch; a second one inside the interval does not.
	for range 2 {
		_, err := v.Verify(t.Context(), sign(t, ecKey(t, "stranger"), jwa.ES256(), validClaims(user)))
		if !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("Verify with unknown kid = %v, want ErrInvalidToken", err)
		}
	}
	if got := srv.fetches.Load(); got != 2 {
		t.Errorf("fetches after two unknown kids = %d, want 2", got)
	}
}

func TestVerifyPicksUpARotatedKey(t *testing.T) {
	old := ecKey(t, "old")
	srv := newJWKSServer(t, old)
	v := newVerifier(t, srv, 0)
	user := uuid.New()
	if _, err := v.Verify(t.Context(), sign(t, old, jwa.ES256(), validClaims(user))); err != nil {
		t.Fatal(err)
	}

	rotated := ecKey(t, "new")
	srv.publish(rotated)
	if _, err := v.Verify(t.Context(), sign(t, rotated, jwa.ES256(), validClaims(user))); err != nil {
		t.Errorf("Verify with rotated key: %v", err)
	}
}

func TestVerifyReportsAnUnreachableJWKSAsAnInfrastructureError(t *testing.T) {
	key := ecKey(t, "ec-1")
	srv := newJWKSServer(t, key)
	v := newVerifier(t, srv, 0)
	srv.Close()

	_, err := v.Verify(t.Context(), sign(t, key, jwa.ES256(), validClaims(uuid.New())))
	if err == nil || errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("Verify = %v, want a non-token error so the caller does not sign the user out", err)
	}
}

func TestSupabaseOptionsDeriveJWKSAndIssuerFromTheProjectURL(t *testing.T) {
	o := auth.SupabaseOptions("http://127.0.0.1:54321/")
	if o.JWKSURL != "http://127.0.0.1:54321/auth/v1/.well-known/jwks.json" || o.Issuer != issuer || o.Audience != audience {
		t.Errorf("options = %+v", o)
	}
}
