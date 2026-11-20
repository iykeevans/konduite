package members_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/members"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

type invite struct {
	members.Invitation
	hash     string
	accepted bool
}

// fakeRepo keeps memberships and invitations in memory and applies the same
// guard rails as the Postgres repository. It is also the authz.Memberships
// the real authorizer reads, so tests exercise the real role matrix.
type fakeRepo struct {
	mu      sync.Mutex
	members map[uuid.UUID][]members.Member
	invites []*invite
	writes  int
}

func newFakeRepo() *fakeRepo { return &fakeRepo{members: map[uuid.UUID][]members.Member{}} }

func (f *fakeRepo) Role(_ context.Context, ws, user uuid.UUID) (authz.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := f.find(ws, user); m != nil {
		return m.Role, nil
	}
	return "", authz.ErrNotMember
}

func (f *fakeRepo) find(ws, user uuid.UUID) *members.Member {
	for i := range f.members[ws] {
		if f.members[ws][i].UserID == user {
			return &f.members[ws][i]
		}
	}
	return nil
}

func (f *fakeRepo) owners(ws uuid.UUID) int {
	n := 0
	for _, m := range f.members[ws] {
		if m.Role == authz.RoleOwner {
			n++
		}
	}
	return n
}

func (f *fakeRepo) add(ws, user uuid.UUID, role authz.Role, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[ws] = append(f.members[ws], members.Member{
		UserID: user, Role: role, Email: email, JoinedAt: time.Now().Add(time.Duration(len(f.members[ws])) * time.Second),
	})
}

func (f *fakeRepo) ListMembers(_ context.Context, ws uuid.UUID, after members.MemberCursor, limit int) ([]members.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []members.Member
	for _, m := range f.members[ws] {
		if m.JoinedAt.After(after.JoinedAt) || (m.JoinedAt.Equal(after.JoinedAt) && m.UserID.String() > after.UserID.String()) {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) ChangeRole(_ context.Context, c members.RoleChange) (members.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	actor, target := f.find(c.WorkspaceID, c.ActorID), f.find(c.WorkspaceID, c.UserID)
	if actor == nil || target == nil {
		return members.Member{}, members.ErrMemberNotFound
	}
	if err := members.CheckRoleChange(actor.Role, target.Role, c.Role, f.owners(c.WorkspaceID)); err != nil {
		return members.Member{}, err
	}
	target.Role = c.Role
	f.writes++
	return *target, nil
}

func (f *fakeRepo) RemoveMember(_ context.Context, r members.Removal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	actor, target := f.find(r.WorkspaceID, r.ActorID), f.find(r.WorkspaceID, r.UserID)
	if actor == nil || target == nil {
		return members.ErrMemberNotFound
	}
	if err := members.CheckRemoval(actor.Role, target.Role, f.owners(r.WorkspaceID), r.ActorID == r.UserID); err != nil {
		return err
	}
	f.members[r.WorkspaceID] = slices.DeleteFunc(f.members[r.WorkspaceID], func(m members.Member) bool { return m.UserID == r.UserID })
	f.writes++
	return nil
}

func (f *fakeRepo) CreateInvitation(_ context.Context, n members.NewInvitation) (members.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.members[n.WorkspaceID] {
		if m.Email == n.Email {
			return members.Invitation{}, members.ErrAlreadyMember
		}
	}
	for _, inv := range f.invites {
		if inv.WorkspaceID == n.WorkspaceID && inv.Email == n.Email && !inv.accepted && inv.ExpiresAt.After(time.Now()) {
			return members.Invitation{}, members.ErrInvitationPending
		}
	}
	inv := &invite{Invitation: members.Invitation{
		ID: uuid.New(), WorkspaceID: n.WorkspaceID, Email: n.Email, Role: n.Role,
		InvitedBy: n.InvitedBy, ExpiresAt: n.ExpiresAt, CreatedAt: time.Now(),
	}, hash: n.TokenHash}
	f.invites = append(f.invites, inv)
	f.writes++
	return inv.Invitation, nil
}

func (f *fakeRepo) ListInvitations(_ context.Context, ws uuid.UUID, _ members.InvitationCursor, limit int) ([]members.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []members.Invitation
	for _, inv := range f.invites {
		if inv.WorkspaceID == ws && !inv.accepted {
			out = append(out, inv.Invitation)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) RevokeInvitation(_ context.Context, ws, id, _ uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, inv := range f.invites {
		if inv.ID == id && inv.WorkspaceID == ws && !inv.accepted {
			f.invites = slices.Delete(f.invites, i, i+1)
			f.writes++
			return nil
		}
	}
	return members.ErrInvitationNotFound
}

func (f *fakeRepo) AcceptInvitation(_ context.Context, a members.Acceptance) (members.Joined, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, inv := range f.invites {
		if inv.hash != a.TokenHash {
			continue
		}
		if inv.accepted || !inv.ExpiresAt.After(time.Now()) {
			return members.Joined{}, members.ErrInvitationNotFound
		}
		if inv.Email != a.Email {
			return members.Joined{}, members.ErrEmailMismatch
		}
		if f.find(inv.WorkspaceID, a.UserID) != nil {
			return members.Joined{}, members.ErrAlreadyMember
		}
		inv.accepted = true
		f.members[inv.WorkspaceID] = append(f.members[inv.WorkspaceID], members.Member{
			UserID: a.UserID, Role: inv.Role, Email: a.Email, JoinedAt: time.Now(),
		})
		f.writes++
		return members.Joined{WorkspaceID: inv.WorkspaceID, Role: inv.Role}, nil
	}
	return members.Joined{}, members.ErrInvitationNotFound
}

type fixture struct {
	svc   *members.Service
	repo  *fakeRepo
	ws    uuid.UUID
	owner auth.Principal
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	repo := newFakeRepo()
	f := fixture{svc: members.NewService(repo, authz.New(repo)), repo: repo, ws: uuid.New(), owner: newUser("owner@example.com")}
	repo.add(f.ws, f.owner.ID, authz.RoleOwner, f.owner.Email)
	return f
}

func newUser(email string) auth.Principal {
	return auth.Principal{Kind: auth.KindUser, ID: uuid.New(), Email: email}
}

func (f fixture) member(role authz.Role, email string) auth.Principal {
	p := newUser(email)
	f.repo.add(f.ws, p.ID, role, email)
	return p
}

func (f fixture) invite(t *testing.T, email string, role authz.Role) members.IssuedInvitation {
	t.Helper()
	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: email, Role: string(role)})
	if err != nil {
		t.Fatalf("invite %s: %v", email, err)
	}
	return inv
}

func wantKind(t *testing.T, err error, kind errs.Kind) {
	t.Helper()
	if got := kindOf(err); got != kind {
		t.Fatalf("err = %v, want kind %q", err, kind)
	}
}

func TestEveryMemberListsMembersPageByPage(t *testing.T) {
	f := newFixture(t)
	devP := f.member(dev, "dev@example.com")
	f.member(pm, "pm@example.com")

	first, err := f.svc.ListMembers(t.Context(), devP, f.ws, members.MemberCursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Next == nil || first.Items[0].UserID != f.owner.ID {
		t.Fatalf("first page = %+v next %v, want owner and developer and a next cursor", first.Items, first.Next)
	}
	second, err := f.svc.ListMembers(t.Context(), devP, f.ws, *first.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Email != "pm@example.com" || second.Next != nil {
		t.Errorf("second page = %+v next %v, want only the project manager", second.Items, second.Next)
	}
}

func TestNonMembersCannotSeeOrManageMembers(t *testing.T) {
	f := newFixture(t)
	stranger := newUser("stranger@example.com")
	target := f.member(dev, "dev@example.com")

	_, err := f.svc.ListMembers(t.Context(), stranger, f.ws, members.MemberCursor{}, 10)
	wantKind(t, err, errs.KindNotFound)
	_, err = f.svc.Invite(t.Context(), stranger, f.ws, members.InviteInput{Email: "x@example.com", Role: "developer"})
	wantKind(t, err, errs.KindNotFound)
	_, err = f.svc.ChangeRole(t.Context(), stranger, f.ws, target.ID, "admin")
	wantKind(t, err, errs.KindNotFound)
	wantKind(t, f.svc.RemoveMember(t.Context(), stranger, f.ws, target.ID), errs.KindNotFound)
	_, err = f.svc.ListInvitations(t.Context(), stranger, f.ws, members.InvitationCursor{}, 10)
	wantKind(t, err, errs.KindNotFound)
	if f.repo.writes != 0 {
		t.Error("a non-member changed something")
	}
}

func TestOnlyOwnersAndAdminsManageMembers(t *testing.T) {
	tests := []struct {
		role authz.Role
		want errs.Kind
	}{
		{owner, ""},
		{admin, ""},
		{dev, errs.KindForbidden},
		{pm, errs.KindForbidden},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			f := newFixture(t)
			caller := f.owner
			if tt.role != owner {
				caller = f.member(tt.role, "caller@example.com")
			}
			target := f.member(dev, "target@example.com")
			ctx := t.Context()

			_, err := f.svc.Invite(ctx, caller, f.ws, members.InviteInput{Email: "new@example.com", Role: "developer"})
			wantKind(t, err, tt.want)
			_, err = f.svc.ListInvitations(ctx, caller, f.ws, members.InvitationCursor{}, 10)
			wantKind(t, err, tt.want)
			_, err = f.svc.ChangeRole(ctx, caller, f.ws, target.ID, "project_manager")
			wantKind(t, err, tt.want)
			wantKind(t, f.svc.RemoveMember(ctx, caller, f.ws, target.ID), tt.want)
		})
	}
}

func TestInviteReturnsTheTokenOnceAndStoresOnlyItsHash(t *testing.T) {
	f := newFixture(t)

	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "  Ada@Example.com ", Role: "developer"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Email != "ada@example.com" || inv.Role != dev || inv.InvitedBy != f.owner.ID || inv.WorkspaceID != f.ws {
		t.Errorf("invitation = %+v, want a developer invitation for ada@example.com by the owner", inv.Invitation)
	}
	if len(inv.Token) < 40 {
		t.Errorf("token %q is too short to be unguessable", inv.Token)
	}
	if ttl := time.Until(inv.ExpiresAt); ttl < 7*24*time.Hour-time.Minute || ttl > 7*24*time.Hour {
		t.Errorf("expires in %v, want 7 days", ttl)
	}
	sum := sha256.Sum256([]byte(inv.Token))
	stored := f.repo.invites[0].hash
	if stored != hex.EncodeToString(sum[:]) || strings.Contains(stored, inv.Token) {
		t.Errorf("stored %q, want the hex SHA-256 of the token", stored)
	}
	if other := f.invite(t, "bob@example.com", admin); other.Token == inv.Token {
		t.Error("two invitations got the same token")
	}
}

func TestInviteValidatesEmailAndRole(t *testing.T) {
	tests := []struct {
		name, email, role, field string
	}{
		{"missing email", " ", "developer", "email"},
		{"no at sign", "ada.example.com", "developer", "email"},
		{"display name", "Ada <ada@example.com>", "developer", "email"},
		{"too long", strings.Repeat("a", 250) + "@example.com", "developer", "email"},
		{"missing role", "ada@example.com", "", "role"},
		{"owner role", "ada@example.com", "owner", "role"},
		{"unknown role", "ada@example.com", "superuser", "role"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: tt.email, Role: tt.role})
			e, ok := errs.As(err)
			if !ok || e.Kind != errs.KindInvalid || e.Fields[tt.field] == "" {
				t.Fatalf("err = %v, want invalid %s", err, tt.field)
			}
		})
	}
}

func TestInvitingAnExistingMemberOrPendingEmailIsAConflict(t *testing.T) {
	f := newFixture(t)
	f.member(dev, "dev@example.com")
	f.invite(t, "pending@example.com", dev)

	for _, email := range []string{"owner@example.com", "DEV@example.com", "pending@example.com"} {
		_, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: email, Role: "developer"})
		wantKind(t, err, errs.KindConflict)
	}
}

func TestAcceptAddsTheMembershipOnce(t *testing.T) {
	f := newFixture(t)
	inv := f.invite(t, "ada@example.com", pm)
	ada := newUser("ada@example.com")

	joined, err := f.svc.Accept(t.Context(), ada, inv.Token)
	if err != nil {
		t.Fatal(err)
	}
	if joined.WorkspaceID != f.ws || joined.Role != pm {
		t.Errorf("joined = %+v, want the workspace as project manager", joined)
	}
	if role, _ := f.repo.Role(t.Context(), f.ws, ada.ID); role != pm {
		t.Errorf("stored role = %q, want project_manager", role)
	}

	_, err = f.svc.Accept(t.Context(), ada, inv.Token)
	wantKind(t, err, errs.KindNotFound)
	_, err = f.svc.Accept(t.Context(), newUser("ada@example.com"), inv.Token)
	wantKind(t, err, errs.KindNotFound)
}

func TestAcceptRejections(t *testing.T) {
	tests := []struct {
		name   string
		caller func(f fixture) auth.Principal
		token  func(inv members.IssuedInvitation) string
		want   errs.Kind
	}{
		{"signed out", func(fixture) auth.Principal { return auth.Principal{} },
			func(inv members.IssuedInvitation) string { return inv.Token }, errs.KindUnauthenticated},
		{"not a user", func(fixture) auth.Principal { return auth.Principal{Kind: "runtime", ID: uuid.New()} },
			func(inv members.IssuedInvitation) string { return inv.Token }, errs.KindForbidden},
		{"no email on the account", func(fixture) auth.Principal { return newUser("") },
			func(inv members.IssuedInvitation) string { return inv.Token }, errs.KindForbidden},
		{"different email", func(fixture) auth.Principal { return newUser("mallory@example.com") },
			func(inv members.IssuedInvitation) string { return inv.Token }, errs.KindForbidden},
		{"unknown token", func(fixture) auth.Principal { return newUser("ada@example.com") },
			func(members.IssuedInvitation) string { return "kdi_unknown" }, errs.KindNotFound},
		{"empty token", func(fixture) auth.Principal { return newUser("ada@example.com") },
			func(members.IssuedInvitation) string { return "" }, errs.KindNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			inv := f.invite(t, "ada@example.com", dev)
			writes := f.repo.writes

			_, err := f.svc.Accept(t.Context(), tt.caller(f), tt.token(inv))

			wantKind(t, err, tt.want)
			if f.repo.writes != writes {
				t.Error("a rejected acceptance changed something")
			}
		})
	}
}

func TestAcceptBySomeoneAlreadyInTheWorkspaceIsAConflict(t *testing.T) {
	f := newFixture(t)
	devP := f.member(dev, "")
	devP.Email = "dev@example.com"
	inv := f.invite(t, "dev@example.com", admin)

	_, err := f.svc.Accept(t.Context(), devP, inv.Token)

	wantKind(t, err, errs.KindConflict)
}

func TestExpiredInvitationsCannotBeAccepted(t *testing.T) {
	f := newFixture(t)
	inv := f.invite(t, "ada@example.com", dev)
	f.repo.invites[0].ExpiresAt = time.Now().Add(-time.Minute)

	_, err := f.svc.Accept(t.Context(), newUser("ada@example.com"), inv.Token)

	wantKind(t, err, errs.KindNotFound)
}

func TestChangeRoleGuardRails(t *testing.T) {
	f := newFixture(t)
	adminP := f.member(admin, "admin@example.com")
	devP := f.member(dev, "dev@example.com")
	ctx := t.Context()

	_, err := f.svc.ChangeRole(ctx, f.owner, f.ws, f.owner.ID, "admin")
	wantKind(t, err, errs.KindConflict)
	_, err = f.svc.ChangeRole(ctx, adminP, f.ws, f.owner.ID, "developer")
	wantKind(t, err, errs.KindForbidden)
	_, err = f.svc.ChangeRole(ctx, adminP, f.ws, devP.ID, "owner")
	wantKind(t, err, errs.KindForbidden)
	_, err = f.svc.ChangeRole(ctx, f.owner, f.ws, devP.ID, "superuser")
	wantKind(t, err, errs.KindInvalid)
	_, err = f.svc.ChangeRole(ctx, f.owner, f.ws, uuid.New(), "admin")
	wantKind(t, err, errs.KindNotFound)

	m, err := f.svc.ChangeRole(ctx, f.owner, f.ws, devP.ID, "owner")
	if err != nil || m.Role != owner {
		t.Fatalf("promote to owner = %+v, %v", m, err)
	}
	if _, err := f.svc.ChangeRole(ctx, f.owner, f.ws, f.owner.ID, "admin"); err != nil {
		t.Errorf("demoting an owner once another exists: %v", err)
	}
}

func TestRemoveMemberGuardRails(t *testing.T) {
	f := newFixture(t)
	adminP := f.member(admin, "admin@example.com")
	devP := f.member(dev, "dev@example.com")
	ctx := t.Context()

	wantKind(t, f.svc.RemoveMember(ctx, adminP, f.ws, f.owner.ID), errs.KindForbidden)
	wantKind(t, f.svc.RemoveMember(ctx, f.owner, f.ws, f.owner.ID), errs.KindConflict)
	wantKind(t, f.svc.RemoveMember(ctx, f.owner, f.ws, uuid.New()), errs.KindNotFound)

	if err := f.svc.RemoveMember(ctx, devP, f.ws, devP.ID); err != nil {
		t.Errorf("a developer leaving: %v", err)
	}
	if err := f.svc.RemoveMember(ctx, adminP, f.ws, adminP.ID); err != nil {
		t.Errorf("an admin leaving: %v", err)
	}
	if _, err := f.repo.Role(ctx, f.ws, devP.ID); err == nil {
		t.Error("the developer is still a member")
	}
}

func TestRevokeInvitation(t *testing.T) {
	f := newFixture(t)
	inv := f.invite(t, "ada@example.com", dev)
	devP := f.member(dev, "dev@example.com")

	wantKind(t, f.svc.RevokeInvitation(t.Context(), devP, f.ws, inv.ID), errs.KindForbidden)
	if err := f.svc.RevokeInvitation(t.Context(), f.owner, f.ws, inv.ID); err != nil {
		t.Fatal(err)
	}
	wantKind(t, f.svc.RevokeInvitation(t.Context(), f.owner, f.ws, inv.ID), errs.KindNotFound)
	_, err := f.svc.Accept(t.Context(), newUser("ada@example.com"), inv.Token)
	wantKind(t, err, errs.KindNotFound)
}

func TestListInvitationsShowsOpenOnesPageByPage(t *testing.T) {
	f := newFixture(t)
	f.invite(t, "a@example.com", dev)
	f.invite(t, "b@example.com", dev)
	used := f.invite(t, "c@example.com", dev)
	if _, err := f.svc.Accept(t.Context(), newUser("c@example.com"), used.Token); err != nil {
		t.Fatal(err)
	}

	page, err := f.svc.ListInvitations(t.Context(), f.owner, f.ws, members.InvitationCursor{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Email != "a@example.com" || page.Next == nil {
		t.Errorf("page = %+v next %v, want a@example.com and a next cursor", page.Items, page.Next)
	}
	all, err := f.svc.ListInvitations(t.Context(), f.owner, f.ws, members.InvitationCursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 2 || all.Next != nil {
		t.Errorf("all = %+v, want the two open invitations", all.Items)
	}
}
