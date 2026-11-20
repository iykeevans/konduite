//go:build integration

package members_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/members"
	"github.com/iykeevans/konduite/backend/internal/platform/dbtest"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
	"github.com/iykeevans/konduite/backend/internal/store"
	"github.com/iykeevans/konduite/backend/internal/workspaces"
)

type pgFixture struct {
	pool  *pgxpool.Pool
	svc   *members.Service
	ws    uuid.UUID
	owner auth.Principal
}

// newPGFixture creates a workspace through the workspaces repository, so
// the owner's email is recorded the way production records it.
func newPGFixture(t *testing.T) pgFixture {
	t.Helper()
	pool := dbtest.New(t)
	owner := newUser("owner@example.com")
	ws, err := workspaces.NewPostgresRepository(pool).Create(t.Context(), workspaces.NewWorkspace{
		Name: "Acme", Slug: "acme", OwnerID: owner.ID, OwnerEmail: owner.Email,
	})
	if err != nil {
		t.Fatal(err)
	}
	az := authz.New(authz.StoreMemberships(store.New(pool)))
	return pgFixture{pool: pool, svc: members.NewService(members.NewPostgresRepository(pool), az), ws: ws.ID, owner: owner}
}

func (f pgFixture) join(t *testing.T, email string, role authz.Role) auth.Principal {
	t.Helper()
	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: email, Role: string(role)})
	if err != nil {
		t.Fatalf("invite %s: %v", email, err)
	}
	p := newUser(email)
	if _, err := f.svc.Accept(t.Context(), p, inv.Token); err != nil {
		t.Fatalf("accept %s: %v", email, err)
	}
	return p
}

type auditRow struct {
	actorID, resourceID uuid.UUID
	action, resource    string
	metadata            map[string]any
}

func (f pgFixture) audit(t *testing.T, action string) []auditRow {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `
		SELECT actor_id, resource_id, action, resource_type, metadata FROM audit_logs
		WHERE workspace_id = $1 AND actor_type = 'user' AND action = $2 ORDER BY created_at`, f.ws, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var (
			a   auditRow
			raw []byte
		)
		if err := rows.Scan(&a.actorID, &a.resourceID, &a.action, &a.resource, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &a.metadata); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f pgFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresInviteAndAcceptAddsTheMemberAndAuditsBothSteps(t *testing.T) {
	f := newPGFixture(t)
	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "Ada@Example.com", Role: "developer"})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM invitations WHERE token_hash = $1 OR token_hash = $2`, inv.Token, hashOf(inv.Token)); n != 1 {
		t.Fatalf("found %d invitations by hash or raw token, want exactly the hashed one", n)
	}
	created := f.audit(t, "invitation.created")
	if len(created) != 1 || created[0].actorID != f.owner.ID || created[0].resourceID != inv.ID ||
		created[0].metadata["email"] != "ada@example.com" || created[0].metadata["role"] != "developer" {
		t.Errorf("invitation.created audit = %+v", created)
	}

	ada := newUser("ada@example.com")
	joined, err := f.svc.Accept(t.Context(), ada, inv.Token)
	if err != nil {
		t.Fatal(err)
	}
	if joined.WorkspaceID != f.ws || joined.Role != dev {
		t.Errorf("joined = %+v", joined)
	}
	role, err := authz.StoreMemberships(store.New(f.pool)).Role(t.Context(), f.ws, ada.ID)
	if err != nil || role != dev {
		t.Errorf("role = %q, %v; want developer", role, err)
	}
	if n := f.count(t, `SELECT count(*) FROM invitations WHERE id = $1 AND accepted_at IS NOT NULL AND accepted_by = $2`, inv.ID, ada.ID); n != 1 {
		t.Error("invitation is not marked accepted by ada")
	}
	j := f.audit(t, "member.joined")
	if len(j) != 1 || j[0].actorID != ada.ID || j[0].resourceID != ada.ID || j[0].metadata["invitation_id"] != inv.ID.String() {
		t.Errorf("member.joined audit = %+v", j)
	}

	_, err = f.svc.Accept(t.Context(), ada, inv.Token)
	wantKind(t, err, errs.KindNotFound)
}

func TestPostgresAcceptRejectsAWrongEmailAndChangesNothing(t *testing.T) {
	f := newPGFixture(t)
	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "ada@example.com", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.svc.Accept(t.Context(), newUser("mallory@example.com"), inv.Token)

	wantKind(t, err, errs.KindForbidden)
	if n := f.count(t, `SELECT count(*) FROM workspace_members WHERE workspace_id = $1`, f.ws); n != 1 {
		t.Errorf("%d members, want only the owner", n)
	}
	if n := f.count(t, `SELECT count(*) FROM invitations WHERE accepted_at IS NULL`); n != 1 {
		t.Error("the invitation was used up by a rejected acceptance")
	}
}

func TestPostgresExpiredInvitationsCannotBeAcceptedButCanBeReissued(t *testing.T) {
	f := newPGFixture(t)
	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "ada@example.com", Role: "developer"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "ada@example.com", Role: "developer"})
	wantKind(t, err, errs.KindConflict)

	if _, err := f.pool.Exec(t.Context(), `UPDATE invitations SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Accept(t.Context(), newUser("ada@example.com"), inv.Token)
	wantKind(t, err, errs.KindNotFound)

	again, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "ada@example.com", Role: "developer"})
	if err != nil {
		t.Fatalf("reissuing after expiry: %v", err)
	}
	if _, err := f.svc.Accept(t.Context(), newUser("ada@example.com"), again.Token); err != nil {
		t.Errorf("accepting the reissued invitation: %v", err)
	}
}

func TestPostgresInvitingAnExistingMemberIsAConflict(t *testing.T) {
	f := newPGFixture(t)
	f.join(t, "dev@example.com", dev)

	for _, email := range []string{"owner@example.com", "dev@example.com"} {
		_, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: email, Role: "admin"})
		wantKind(t, err, errs.KindConflict)
	}
}

func TestPostgresAcceptByAnExistingMemberIsAConflict(t *testing.T) {
	f := newPGFixture(t)
	devP := f.join(t, "dev@example.com", dev)
	inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: "dev-alias@example.com", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	devP.Email = "dev-alias@example.com"

	_, err = f.svc.Accept(t.Context(), devP, inv.Token)

	wantKind(t, err, errs.KindConflict)
}

func TestPostgresMembersListAndRoleChangesAreAudited(t *testing.T) {
	f := newPGFixture(t)
	devP := f.join(t, "dev@example.com", dev)
	f.join(t, "pm@example.com", pm)

	first, err := f.svc.ListMembers(t.Context(), devP, f.ws, members.MemberCursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].UserID != f.owner.ID || first.Items[0].Email != "owner@example.com" || first.Next == nil {
		t.Fatalf("first page = %+v next %v", first.Items, first.Next)
	}
	second, err := f.svc.ListMembers(t.Context(), devP, f.ws, *first.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Email != "pm@example.com" || second.Next != nil {
		t.Errorf("second page = %+v next %v", second.Items, second.Next)
	}

	m, err := f.svc.ChangeRole(t.Context(), f.owner, f.ws, devP.ID, "admin")
	if err != nil || m.Role != admin || m.Email != "dev@example.com" {
		t.Fatalf("change role = %+v, %v", m, err)
	}
	a := f.audit(t, "member.role_changed")
	if len(a) != 1 || a[0].actorID != f.owner.ID || a[0].resourceID != devP.ID || a[0].metadata["from"] != "developer" || a[0].metadata["to"] != "admin" {
		t.Errorf("role_changed audit = %+v", a)
	}

	if _, err := f.svc.ChangeRole(t.Context(), f.owner, f.ws, devP.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.audit(t, "member.role_changed")); n != 1 {
		t.Errorf("%d role_changed rows after a no-op change, want 1", n)
	}
}

func TestPostgresLastOwnerGuardRails(t *testing.T) {
	f := newPGFixture(t)
	adminP := f.join(t, "admin@example.com", admin)

	_, err := f.svc.ChangeRole(t.Context(), f.owner, f.ws, f.owner.ID, "admin")
	wantKind(t, err, errs.KindConflict)
	wantKind(t, f.svc.RemoveMember(t.Context(), f.owner, f.ws, f.owner.ID), errs.KindConflict)
	wantKind(t, f.svc.RemoveMember(t.Context(), adminP, f.ws, f.owner.ID), errs.KindForbidden)
	_, err = f.svc.ChangeRole(t.Context(), adminP, f.ws, adminP.ID, "owner")
	wantKind(t, err, errs.KindForbidden)

	if n := f.count(t, `SELECT count(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'owner'`, f.ws); n != 1 {
		t.Errorf("%d owners, want 1", n)
	}
	if n := len(f.audit(t, "member.role_changed")) + len(f.audit(t, "member.removed")); n != 0 {
		t.Errorf("%d audit rows for rejected changes", n)
	}
}

// Two owners demoting each other at once must not both succeed.
func TestPostgresConcurrentOwnerDemotionsKeepAnOwner(t *testing.T) {
	f := newPGFixture(t)
	second := f.join(t, "second@example.com", admin)
	if _, err := f.svc.ChangeRole(t.Context(), f.owner, f.ws, second.ID, "owner"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errsOut := make([]error, 2)
	for i, pair := range [][2]auth.Principal{{f.owner, second}, {second, f.owner}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errsOut[i] = f.svc.ChangeRole(t.Context(), pair[0], f.ws, pair[1].ID, "admin")
		}()
	}
	wg.Wait()

	if n := f.count(t, `SELECT count(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'owner'`, f.ws); n != 1 {
		t.Errorf("%d owners after concurrent demotions (errors %v), want 1", n, errsOut)
	}
}

func TestPostgresRemoveAndLeaveAreAudited(t *testing.T) {
	f := newPGFixture(t)
	devP := f.join(t, "dev@example.com", dev)
	pmP := f.join(t, "pm@example.com", pm)

	if err := f.svc.RemoveMember(t.Context(), f.owner, f.ws, devP.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveMember(t.Context(), pmP, f.ws, pmP.ID); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM workspace_members WHERE workspace_id = $1`, f.ws); n != 1 {
		t.Errorf("%d members, want only the owner", n)
	}
	a := f.audit(t, "member.removed")
	if len(a) != 2 || a[0].actorID != f.owner.ID || a[0].resourceID != devP.ID || a[0].metadata["self"] != false ||
		a[1].actorID != pmP.ID || a[1].metadata["self"] != true {
		t.Errorf("member.removed audit = %+v", a)
	}

	wantKind(t, f.svc.RemoveMember(t.Context(), f.owner, f.ws, devP.ID), errs.KindNotFound)
}

func TestPostgresListAndRevokeInvitations(t *testing.T) {
	f := newPGFixture(t)
	var ids []uuid.UUID
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		inv, err := f.svc.Invite(t.Context(), f.owner, f.ws, members.InviteInput{Email: email, Role: "developer"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, inv.ID)
	}

	first, err := f.svc.ListInvitations(t.Context(), f.owner, f.ws, members.InvitationCursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].Email != "a@example.com" || first.Next == nil {
		t.Fatalf("first page = %+v", first.Items)
	}
	second, err := f.svc.ListInvitations(t.Context(), f.owner, f.ws, *first.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Email != "c@example.com" || second.Next != nil {
		t.Errorf("second page = %+v", second.Items)
	}

	if err := f.svc.RevokeInvitation(t.Context(), f.owner, f.ws, ids[1]); err != nil {
		t.Fatal(err)
	}
	wantKind(t, f.svc.RevokeInvitation(t.Context(), f.owner, f.ws, ids[1]), errs.KindNotFound)
	r := f.audit(t, "invitation.revoked")
	if len(r) != 1 || r[0].resourceID != ids[1] || r[0].metadata["email"] != "b@example.com" {
		t.Errorf("invitation.revoked audit = %+v", r)
	}

	wantKind(t, f.svc.RevokeInvitation(t.Context(), f.owner, uuid.New(), ids[0]), errs.KindNotFound)
}

func hashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
