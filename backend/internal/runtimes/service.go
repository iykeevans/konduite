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
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	credentialPrefix = "krt_"
	secretSize       = 32
	maxNameLength    = 100
)

// Service registers and lists runtimes and exchanges refresh credentials for
// tokens.
type Service struct {
	repo   Repository
	az     Authorizer
	signer signer
}

// NewService returns a Service that signs runtime tokens with key.
func NewService(repo Repository, az Authorizer, key ed25519.PrivateKey) *Service {
	return &Service{repo: repo, az: az, signer: newSigner(key)}
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
	if err := s.az.Can(ctx, p, authz.ActionRegisterRuntime, authz.Workspace(in.WorkspaceID)); err != nil {
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

// Get returns a runtime. One in a workspace p cannot see gets the same error
// as one that does not exist.
func (s *Service) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Runtime, error) {
	notFound := errs.NotFound("runtime not found")
	rt, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Runtime{}, notFound
	}
	if err != nil {
		return Runtime{}, err
	}
	if err := s.az.Can(ctx, p, authz.ActionViewRuntimes, authz.Workspace(rt.WorkspaceID)); err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
			return Runtime{}, notFound
		}
		return Runtime{}, err
	}
	return rt, nil
}

// List returns a page of in.WorkspaceID's runtimes, newest first.
func (s *Service) List(ctx context.Context, p auth.Principal, in ListInput) (Page, error) {
	if in.WorkspaceID == uuid.Nil {
		return Page{}, errs.Invalid("invalid runtime query", map[string]string{"workspace_id": "required"})
	}
	if err := s.az.Can(ctx, p, authz.ActionViewRuntimes, authz.Workspace(in.WorkspaceID)); err != nil {
		return Page{}, err
	}
	items, err := s.repo.List(ctx, ListFilter{WorkspaceID: in.WorkspaceID, After: in.After, Limit: in.Limit + 1})
	if err != nil {
		return Page{}, err
	}
	if len(items) <= in.Limit {
		return Page{Items: items}, nil
	}
	items = items[:in.Limit]
	last := items[len(items)-1]
	return Page{Items: items, Next: &Position{CreatedAt: last.CreatedAt, ID: last.ID}}, nil
}

// Self returns the runtime a runtime access token was issued to. The token is
// the authorization: it can only ever name its own runtime, so there is no
// workspace role to check. A token whose runtime is gone is unauthenticated.
func (s *Service) Self(ctx context.Context, accessToken string) (Runtime, error) {
	denied := errs.Unauthenticated("invalid runtime access token")
	claims, err := VerifyAccessToken(s.signer.key.Public().(ed25519.PublicKey), accessToken)
	if err != nil {
		return Runtime{}, denied
	}
	rt, err := s.repo.Get(ctx, claims.RuntimeID)
	if errors.Is(err, ErrNotFound) {
		return Runtime{}, denied
	}
	return rt, err
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
