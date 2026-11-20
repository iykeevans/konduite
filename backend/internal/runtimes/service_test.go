package runtimes_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/runtimes"
)

type fakeRepo struct {
	mu          sync.Mutex
	runtimes    map[uuid.UUID]runtimes.Runtime
	credentials map[uuid.UUID]runtimes.Credential
	roles       map[[2]uuid.UUID]authz.Role
	touched     []uuid.UUID
	clock       time.Time
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		runtimes:    map[uuid.UUID]runtimes.Runtime{},
		credentials: map[uuid.UUID]runtimes.Credential{},
		roles:       map[[2]uuid.UUID]authz.Role{},
		clock:       time.Now(),
	}
}

func (f *fakeRepo) Role(_ context.Context, workspaceID, userID uuid.UUID) (authz.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.roles[[2]uuid.UUID{workspaceID, userID}]
	if !ok {
		return "", authz.ErrNotMember
	}
	return role, nil
}

func (f *fakeRepo) Get(_ context.Context, id uuid.UUID) (runtimes.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt, ok := f.runtimes[id]
	if !ok {
		return runtimes.Runtime{}, runtimes.ErrNotFound
	}
	return f.withCredential(rt), nil
}

func (f *fakeRepo) List(_ context.Context, l runtimes.ListFilter) ([]runtimes.Runtime, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runtimes.Runtime
	for _, rt := range f.runtimes {
		if rt.WorkspaceID != l.WorkspaceID {
			continue
		}
		if l.After != nil && !newerFirst(l.After.CreatedAt, l.After.ID, rt.CreatedAt, rt.ID) {
			continue
		}
		out = append(out, f.withCredential(rt))
	}
	slices.SortFunc(out, func(a, b runtimes.Runtime) int {
		if newerFirst(a.CreatedAt, a.ID, b.CreatedAt, b.ID) {
			return -1
		}
		return 1
	})
	if len(out) > l.Limit {
		out = out[:l.Limit]
	}
	return out, nil
}

func newerFirst(at time.Time, aid uuid.UUID, bt time.Time, bid uuid.UUID) bool {
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return bytes.Compare(aid[:], bid[:]) > 0
}

func (f *fakeRepo) withCredential(rt runtimes.Runtime) runtimes.Runtime {
	rt.Revoked = true
	for _, c := range f.credentials {
		if c.RuntimeID == rt.ID {
			rt.Revoked = false
		}
	}
	return rt
}

func (f *fakeRepo) Create(_ context.Context, n runtimes.NewRuntime) (runtimes.Runtime, uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(time.Second)
	rt := runtimes.Runtime{ID: uuid.New(), WorkspaceID: n.WorkspaceID, OwnerID: n.OwnerID, Name: n.Name, CreatedAt: f.clock}
	credID := uuid.New()
	f.runtimes[rt.ID] = rt
	f.credentials[credID] = runtimes.Credential{
		ID: credID, RuntimeID: rt.ID, WorkspaceID: n.WorkspaceID, OwnerID: n.OwnerID, SecretHash: n.SecretHash,
	}
	return rt, credID, nil
}

func (f *fakeRepo) Credential(_ context.Context, id uuid.UUID) (runtimes.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.credentials[id]
	if !ok {
		return runtimes.Credential{}, runtimes.ErrCredentialNotFound
	}
	return c, nil
}

func (f *fakeRepo) TouchCredential(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, id)
	return nil
}

func (f *fakeRepo) DeleteCredential(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.credentials, id)
	return nil
}

type fixture struct {
	svc       *runtimes.Service
	repo      *fakeRepo
	pub       ed25519.PublicKey
	user      auth.Principal
	workspace uuid.UUID
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	user, workspace := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}, uuid.New()
	repo := newFakeRepo()
	repo.roles[[2]uuid.UUID{workspace, user.ID}] = authz.RoleDeveloper
	return fixture{
		svc:  runtimes.NewService(repo, authz.New(repo), priv),
		repo: repo, pub: pub, user: user, workspace: workspace,
	}
}

func wantKind(t *testing.T, err error, kind errs.Kind) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Kind != kind {
		t.Fatalf("err = %v, want kind %s", err, kind)
	}
}

func TestMemberRegistersARuntimeAndGetsARefreshCredential(t *testing.T) {
	f := newFixture(t)

	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: " laptop "})
	if err != nil {
		t.Fatal(err)
	}
	if reg.WorkspaceID != f.workspace || reg.OwnerID != f.user.ID || reg.Name != "laptop" || reg.RuntimeID == uuid.Nil {
		t.Errorf("registration = %+v, want laptop in the caller's workspace, owned by the caller", reg)
	}
	if !strings.HasPrefix(reg.RefreshCredential, "krt_") || len(reg.RefreshCredential) < 60 {
		t.Errorf("refresh credential %q, want a long krt_ token", reg.RefreshCredential)
	}
	for _, c := range f.repo.credentials {
		if strings.Contains(reg.RefreshCredential, string(c.SecretHash)) || len(c.SecretHash) != 32 {
			t.Errorf("stored secret hash %x, want a 32-byte hash that is not the credential", c.SecretHash)
		}
	}
}

func TestEachRegistrationGetsADifferentCredential(t *testing.T) {
	f := newFixture(t)
	in := runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"}
	a, err := f.svc.Register(t.Context(), f.user, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Register(t.Context(), f.user, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.RefreshCredential == b.RefreshCredential || a.RuntimeID == b.RuntimeID {
		t.Error("two registrations share a runtime or credential")
	}
}

func TestRegisterIsDeniedOutsideTheCallersWorkspace(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: uuid.New(), Name: "laptop"})
	wantKind(t, err, errs.KindNotFound)
	if len(f.repo.runtimes) != 0 {
		t.Error("a runtime was stored for a forbidden registration")
	}
}

func TestRegisterValidatesItsInput(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name  string
		in    runtimes.RegisterInput
		field string
	}{
		{"blank name", runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "   "}, "name"},
		{"long name", runtimes.RegisterInput{WorkspaceID: f.workspace, Name: strings.Repeat("a", 101)}, "name"},
		{"no workspace", runtimes.RegisterInput{Name: "laptop"}, "workspace_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Register(t.Context(), f.user, tt.in)
			wantKind(t, err, errs.KindInvalid)
			if e, _ := errs.As(err); e.Fields[tt.field] == "" {
				t.Errorf("fields = %v, want a problem with %s", e.Fields, tt.field)
			}
		})
	}
}

func TestRefreshCredentialExchangesForA15MinuteRuntimeToken(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now().Truncate(time.Second)
	tok, err := f.svc.Exchange(t.Context(), reg.RefreshCredential)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	claims, err := runtimes.VerifyAccessToken(f.pub, tok.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.RuntimeID != reg.RuntimeID || claims.WorkspaceID != f.workspace || claims.OwnerID != f.user.ID {
		t.Errorf("claims = %+v, want the registered runtime, workspace and owner", claims)
	}
	if claims.ExpiresAt.Before(before.Add(15*time.Minute)) || claims.ExpiresAt.After(after.Add(15*time.Minute)) {
		t.Errorf("expires at %v, want 15 minutes after %v", claims.ExpiresAt, before)
	}
	if !tok.ExpiresAt.Equal(claims.ExpiresAt) {
		t.Errorf("reported expiry %v, token says %v", tok.ExpiresAt, claims.ExpiresAt)
	}
	if len(f.repo.touched) != 1 {
		t.Errorf("credential marked used %d times, want 1", len(f.repo.touched))
	}
}

func TestExchangeRejectsBadCredentials(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	other := newFixture(t)
	foreign, err := other.svc.Register(t.Context(), other.user, runtimes.RegisterInput{WorkspaceID: other.workspace, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	last := reg.RefreshCredential[len(reg.RefreshCredential)-2:]
	flipped := reg.RefreshCredential[:len(reg.RefreshCredential)-2] + map[bool]string{true: "AA", false: "BB"}[last != "AA"]

	for name, cred := range map[string]string{
		"empty":            "",
		"no prefix":        strings.TrimPrefix(reg.RefreshCredential, "krt_"),
		"not base64":       "krt_!!!!",
		"truncated":        reg.RefreshCredential[:30],
		"wrong secret":     flipped,
		"unknown runtime":  foreign.RefreshCredential,
		"supabase-ish jwt": "eyJhbGciOiJFUzI1NiJ9.e30.sig",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Exchange(t.Context(), cred)
			wantKind(t, err, errs.KindUnauthenticated)
		})
	}
}

func TestRuntimeTokenFromAnotherKeyIsRejected(t *testing.T) {
	f, other := newFixture(t), newFixture(t)
	reg, err := other.svc.Register(t.Context(), other.user, runtimes.RegisterInput{WorkspaceID: other.workspace, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := other.svc.Exchange(t.Context(), reg.RefreshCredential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimes.VerifyAccessToken(f.pub, tok.Token); err == nil {
		t.Error("a token signed by another key verified")
	}
}

func TestExpiredRuntimeTokenIsRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	tok, err := jwt.NewBuilder().Issuer("konduite").Audience([]string{"konduite-runtime"}).
		IssuedAt(past).Expiration(past.Add(runtimes.TokenTTL)).
		Claim("runtime_id", uuid.NewString()).Claim("workspace_id", uuid.NewString()).Claim("owner_id", uuid.NewString()).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), priv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimes.VerifyAccessToken(pub, string(signed)); err == nil {
		t.Error("an expired token verified")
	}
}

func TestRevokedCredentialNoLongerExchanges(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.svc.Revoke(t.Context(), reg.RefreshCredential); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if len(f.repo.credentials) != 0 {
		t.Errorf("%d credentials left after revoke, want 0", len(f.repo.credentials))
	}
	_, err = f.svc.Exchange(t.Context(), reg.RefreshCredential)
	wantKind(t, err, errs.KindUnauthenticated)
	err = f.svc.Revoke(t.Context(), reg.RefreshCredential)
	wantKind(t, err, errs.KindUnauthenticated)
}

func TestRevokeRejectsBadCredentialsAndKeepsTheRealOne(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	last := reg.RefreshCredential[len(reg.RefreshCredential)-2:]
	flipped := reg.RefreshCredential[:len(reg.RefreshCredential)-2] + map[bool]string{true: "AA", false: "BB"}[last != "AA"]

	for name, cred := range map[string]string{
		"empty":        "",
		"not base64":   "krt_!!!!",
		"wrong secret": flipped,
	} {
		t.Run(name, func(t *testing.T) {
			wantKind(t, f.svc.Revoke(t.Context(), cred), errs.KindUnauthenticated)
		})
	}
	if _, err := f.svc.Exchange(t.Context(), reg.RefreshCredential); err != nil {
		t.Errorf("real credential stopped working after bad revokes: %v", err)
	}
}

func TestMembersSeeTheirWorkspacesRuntimesWithoutCredentials(t *testing.T) {
	f := newFixture(t)
	viewer := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	f.repo.roles[[2]uuid.UUID{f.workspace, viewer.ID}] = authz.RoleProjectManager
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}

	rt, err := f.svc.Get(t.Context(), viewer, reg.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if rt.ID != reg.RuntimeID || rt.WorkspaceID != f.workspace || rt.OwnerID != f.user.ID || rt.Name != "laptop" || rt.Revoked {
		t.Errorf("runtime = %+v, want the registered, unrevoked laptop", rt)
	}

	if err := f.svc.Revoke(t.Context(), reg.RefreshCredential); err != nil {
		t.Fatal(err)
	}
	rt, err = f.svc.Get(t.Context(), viewer, reg.RuntimeID)
	if err != nil || !rt.Revoked {
		t.Errorf("after revoke: runtime = %+v, %v, want revoked", rt, err)
	}
}

func TestGetHidesRuntimesOutsideTheCallersWorkspaces(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	stranger := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}

	_, hidden := f.svc.Get(t.Context(), stranger, reg.RuntimeID)
	_, missing := f.svc.Get(t.Context(), f.user, uuid.New())

	wantKind(t, hidden, errs.KindNotFound)
	wantKind(t, missing, errs.KindNotFound)
	if hidden.Error() != missing.Error() {
		t.Errorf("hidden %q and missing %q differ, so a stranger can tell they exist", hidden, missing)
	}
}

func TestListPagesThroughAWorkspacesRuntimesNewestFirst(t *testing.T) {
	f := newFixture(t)
	var want []uuid.UUID
	for _, name := range []string{"a", "b", "c"} {
		reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		want = append([]uuid.UUID{reg.RuntimeID}, want...)
	}
	other := uuid.New()
	f.repo.roles[[2]uuid.UUID{other, f.user.ID}] = authz.RoleOwner
	if _, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: other, Name: "elsewhere"}); err != nil {
		t.Fatal(err)
	}

	first, err := f.svc.List(t.Context(), f.user, runtimes.ListInput{WorkspaceID: f.workspace, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Next == nil {
		t.Fatalf("first page = %d items, next %v; want 2 and a cursor", len(first.Items), first.Next)
	}
	second, err := f.svc.List(t.Context(), f.user, runtimes.ListInput{WorkspaceID: f.workspace, Limit: 2, After: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Next != nil {
		t.Fatalf("second page = %d items, next %v; want 1 and no cursor", len(second.Items), second.Next)
	}
	var got []uuid.UUID
	for _, rt := range append(first.Items, second.Items...) {
		got = append(got, rt.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestListRejects(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.List(t.Context(), f.user, runtimes.ListInput{Limit: 10})
	wantKind(t, err, errs.KindInvalid)

	stranger := auth.Principal{Kind: auth.KindUser, ID: uuid.New()}
	_, err = f.svc.List(t.Context(), stranger, runtimes.ListInput{WorkspaceID: f.workspace, Limit: 10})
	wantKind(t, err, errs.KindNotFound)
}

func TestARuntimeReadsItsOwnRowWithItsAccessToken(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.svc.Exchange(t.Context(), reg.RefreshCredential)
	if err != nil {
		t.Fatal(err)
	}

	rt, err := f.svc.Self(t.Context(), tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if rt.ID != reg.RuntimeID || rt.Name != "laptop" {
		t.Errorf("self = %+v, want the token's runtime", rt)
	}
}

func TestSelfRejectsTokensItDidNotIssue(t *testing.T) {
	f, other := newFixture(t), newFixture(t)
	reg, err := other.svc.Register(t.Context(), other.user, runtimes.RegisterInput{WorkspaceID: other.workspace, Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.svc.Exchange(t.Context(), reg.RefreshCredential)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"empty":           "",
		"garbage":         "not-a-jwt",
		"another key":     foreign.Token,
		"refresh instead": reg.RefreshCredential,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Self(t.Context(), tok)
			wantKind(t, err, errs.KindUnauthenticated)
		})
	}
}

func TestSelfRejectsATokenForADeletedRuntime(t *testing.T) {
	f := newFixture(t)
	reg, err := f.svc.Register(t.Context(), f.user, runtimes.RegisterInput{WorkspaceID: f.workspace, Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.svc.Exchange(t.Context(), reg.RefreshCredential)
	if err != nil {
		t.Fatal(err)
	}
	delete(f.repo.runtimes, reg.RuntimeID)

	_, err = f.svc.Self(t.Context(), tok.Token)
	wantKind(t, err, errs.KindUnauthenticated)
}
