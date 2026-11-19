package runtimes

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// TokenTTL is how long a runtime access token is valid.
const TokenTTL = 15 * time.Minute

const (
	runtimeIssuer   = "konduite"
	runtimeAudience = "konduite-runtime"
)

type signer struct {
	key ed25519.PrivateKey
	kid string
}

func newSigner(key ed25519.PrivateKey) signer {
	return signer{key: key, kid: KeyID(key.Public().(ed25519.PublicKey))}
}

// KeyID names a signing key in a token's kid header without revealing it.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func (s signer) sign(c Credential) (AccessToken, error) {
	now := time.Now().Truncate(time.Second)
	exp := now.Add(TokenTTL)
	tok, err := jwt.NewBuilder().
		Issuer(runtimeIssuer).
		Audience([]string{runtimeAudience}).
		Subject(c.RuntimeID.String()).
		IssuedAt(now).
		Expiration(exp).
		JwtID(uuid.NewString()).
		Claim("runtime_id", c.RuntimeID.String()).
		Claim("workspace_id", c.WorkspaceID.String()).
		Claim("owner_id", c.OwnerID.String()).
		Build()
	if err != nil {
		return AccessToken{}, err
	}
	hdr := jws.NewHeaders()
	if err := hdr.Set(jws.KeyIDKey, s.kid); err != nil {
		return AccessToken{}, err
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), s.key, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		return AccessToken{}, err
	}
	return AccessToken{Token: string(signed), ExpiresAt: exp}, nil
}

// VerifyAccessToken checks a runtime access token's signature, issuer,
// audience and expiry against pub and returns its claims.
func VerifyAccessToken(pub ed25519.PublicKey, token string) (Claims, error) {
	tok, err := jwt.ParseString(token,
		jwt.WithKey(jwa.EdDSA(), pub),
		jwt.WithIssuer(runtimeIssuer),
		jwt.WithAudience(runtimeAudience),
		jwt.WithRequiredClaim("exp"),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("runtimes: invalid access token: %w", err)
	}
	var c Claims
	for name, dst := range map[string]*uuid.UUID{
		"runtime_id": &c.RuntimeID, "workspace_id": &c.WorkspaceID, "owner_id": &c.OwnerID,
	} {
		var raw string
		if err := tok.Get(name, &raw); err != nil {
			return Claims{}, fmt.Errorf("runtimes: access token has no %s", name)
		}
		if *dst, err = uuid.Parse(raw); err != nil || *dst == uuid.Nil {
			return Claims{}, fmt.Errorf("runtimes: access token %s is not an id", name)
		}
	}
	exp, ok := tok.Expiration()
	if !ok {
		return Claims{}, errors.New("runtimes: access token has no expiry")
	}
	c.ExpiresAt = exp
	return c, nil
}
