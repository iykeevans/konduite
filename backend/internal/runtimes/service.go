package runtimes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	credentialPrefix = "krt_"
	secretSize       = 32
	maxNameLength    = 100
)

// Service registers runtimes and exchanges refresh credentials for tokens.
type Service struct {
	repo      Repository
	authorize AuthorizeRegister
	signer    signer
}

// NewService returns a Service that signs runtime tokens with key.
func NewService(repo Repository, authorize AuthorizeRegister, key ed25519.PrivateKey) *Service {
	return &Service{repo: repo, authorize: authorize, signer: newSigner(key)}
}

// Register creates a runtime owned by p in in.WorkspaceID and returns its
// refresh credential, once authorize allows it.
func (s *Service) Register(ctx context.Context, p auth.Principal, in RegisterInput) (Registration, error) {
	name := strings.TrimSpace(in.Name)
	fields := map[string]string{}
	if in.WorkspaceID == uuid.Nil {
		fields["workspace_id"] = "required"
	}
	if name == "" {
		fields["name"] = "required"
	} else if utf8.RuneCountInString(name) > maxNameLength {
		fields["name"] = "must be at most 100 characters"
	}
	if len(fields) > 0 {
		return Registration{}, errs.Invalid("invalid runtime registration", fields)
	}
	if err := s.authorize(ctx, p, in.WorkspaceID); err != nil {
		return Registration{}, err
	}

	secret := make([]byte, secretSize)
	if _, err := rand.Read(secret); err != nil {
		return Registration{}, err
	}
	hash := sha256.Sum256(secret)
	rt, credID, err := s.repo.Create(ctx, NewRuntime{
		WorkspaceID: in.WorkspaceID, OwnerID: p.ID, Name: name, SecretHash: hash[:],
	})
	if err != nil {
		return Registration{}, err
	}
	return Registration{
		RuntimeID:         rt.ID,
		WorkspaceID:       rt.WorkspaceID,
		OwnerID:           rt.OwnerID,
		Name:              rt.Name,
		RefreshCredential: encodeCredential(credID, secret),
		CreatedAt:         rt.CreatedAt,
	}, nil
}

// Exchange returns a fresh runtime access token for a refresh credential.
// Every kind of bad credential gets the same errs.Unauthenticated error.
func (s *Service) Exchange(ctx context.Context, refreshCredential string) (AccessToken, error) {
	cred, err := s.verify(ctx, refreshCredential)
	if err != nil {
		return AccessToken{}, err
	}
	if err := s.repo.TouchCredential(ctx, cred.ID); err != nil {
		return AccessToken{}, err
	}
	return s.signer.sign(cred)
}

// Revoke deletes a refresh credential so it can no longer be exchanged.
// Access tokens already issued for it stay valid until they expire.
func (s *Service) Revoke(ctx context.Context, refreshCredential string) error {
	cred, err := s.verify(ctx, refreshCredential)
	if err != nil {
		return err
	}
	return s.repo.DeleteCredential(ctx, cred.ID)
}

func (s *Service) verify(ctx context.Context, refreshCredential string) (Credential, error) {
	denied := errs.Unauthenticated("invalid refresh credential")
	id, secret, ok := decodeCredential(refreshCredential)
	if !ok {
		return Credential{}, denied
	}
	cred, err := s.repo.Credential(ctx, id)
	if errors.Is(err, ErrCredentialNotFound) {
		return Credential{}, denied
	}
	if err != nil {
		return Credential{}, err
	}
	hash := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare(hash[:], cred.SecretHash) != 1 {
		return Credential{}, denied
	}
	return cred, nil
}

func encodeCredential(id uuid.UUID, secret []byte) string {
	return credentialPrefix + base64.RawURLEncoding.EncodeToString(append(id[:], secret...))
}

func decodeCredential(s string) (uuid.UUID, []byte, bool) {
	rest, ok := strings.CutPrefix(s, credentialPrefix)
	if !ok {
		return uuid.Nil, nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) != len(uuid.UUID{})+secretSize {
		return uuid.Nil, nil, false
	}
	id, err := uuid.FromBytes(raw[:16])
	if err != nil {
		return uuid.Nil, nil, false
	}
	return id, raw[16:], true
}
