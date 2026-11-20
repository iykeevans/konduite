package members

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	invitationTTL  = 7 * 24 * time.Hour
	tokenPrefix    = "kdi_"
	tokenBytes     = 32
	maxEmailLength = 254
)

// InviteInput is what an owner or admin supplies to invite someone.
type InviteInput struct {
	Email string
	Role  string
}

// Service is the membership use cases. Every method takes the caller from
// the verified token, never from the request body.
type Service struct {
	repo Repository
	az   Authorizer
}

// NewService returns a Service storing memberships in repo and checking
// permissions with az.
func NewService(repo Repository, az Authorizer) *Service {
	return &Service{repo: repo, az: az}
}

// ListMembers returns a page of workspace ws's members to any member.
func (s *Service) ListMembers(ctx context.Context, p auth.Principal, ws uuid.UUID, after MemberCursor, limit int) (MemberPage, error) {
	if err := s.az.Can(ctx, p, authz.ActionViewMembers, authz.Workspace(ws)); err != nil {
		return MemberPage{}, err
	}
	items, err := s.repo.ListMembers(ctx, ws, after, limit+1)
	if err != nil {
		return MemberPage{}, err
	}
	page := MemberPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &MemberCursor{JoinedAt: last.JoinedAt, UserID: last.UserID}
	}
	return page, nil
}

// ChangeRole gives member userID a new role. Owners and admins may, within
// CheckRoleChange's limits.
func (s *Service) ChangeRole(ctx context.Context, p auth.Principal, ws, userID uuid.UUID, role string) (Member, error) {
	next := authz.Role(strings.TrimSpace(role))
	if !isRole(next) {
		return Member{}, errs.Invalid("invalid role", map[string]string{"role": "must be owner, admin, developer or project_manager"})
	}
	if err := s.az.Can(ctx, p, authz.ActionManageMembers, authz.Workspace(ws)); err != nil {
		return Member{}, err
	}
	m, err := s.repo.ChangeRole(ctx, RoleChange{WorkspaceID: ws, ActorID: p.ID, UserID: userID, Role: next})
	return m, domainError(err)
}

// RemoveMember removes userID from the workspace. Owners and admins may
// remove others, within CheckRemoval's limits; any member may leave.
func (s *Service) RemoveMember(ctx context.Context, p auth.Principal, ws, userID uuid.UUID) error {
	action := authz.ActionManageMembers
	if userID == p.ID {
		action = authz.ActionViewWorkspace
	}
	if err := s.az.Can(ctx, p, action, authz.Workspace(ws)); err != nil {
		return err
	}
	return domainError(s.repo.RemoveMember(ctx, Removal{WorkspaceID: ws, ActorID: p.ID, UserID: userID}))
}

// Invite creates an invitation for email and returns its token. There is no
// mail provider yet: the inviter passes the accept link on themselves.
func (s *Service) Invite(ctx context.Context, p auth.Principal, ws uuid.UUID, in InviteInput) (IssuedInvitation, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	role := authz.Role(strings.TrimSpace(in.Role))
	fields := map[string]string{}
	if msg := checkEmail(email); msg != "" {
		fields["email"] = msg
	}
	if !isRole(role) || role == authz.RoleOwner {
		fields["role"] = "must be admin, developer or project_manager"
	}
	if len(fields) > 0 {
		return IssuedInvitation{}, errs.Invalid("invalid invitation", fields)
	}
	if err := s.az.Can(ctx, p, authz.ActionManageMembers, authz.Workspace(ws)); err != nil {
		return IssuedInvitation{}, err
	}

	secret := make([]byte, tokenBytes)
	if _, err := rand.Read(secret); err != nil {
		return IssuedInvitation{}, err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	inv, err := s.repo.CreateInvitation(ctx, NewInvitation{
		WorkspaceID: ws, Email: email, Role: role, TokenHash: hashToken(token),
		InvitedBy: p.ID, ExpiresAt: time.Now().Add(invitationTTL),
	})
	if err != nil {
		return IssuedInvitation{}, domainError(err)
	}
	return IssuedInvitation{Invitation: inv, Token: token}, nil
}

// ListInvitations returns a page of open invitations to owners and admins.
func (s *Service) ListInvitations(ctx context.Context, p auth.Principal, ws uuid.UUID, after InvitationCursor, limit int) (InvitationPage, error) {
	if err := s.az.Can(ctx, p, authz.ActionManageMembers, authz.Workspace(ws)); err != nil {
		return InvitationPage{}, err
	}
	items, err := s.repo.ListInvitations(ctx, ws, after, limit+1)
	if err != nil {
		return InvitationPage{}, err
	}
	page := InvitationPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &InvitationCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// RevokeInvitation deletes an open invitation so its token stops working.
func (s *Service) RevokeInvitation(ctx context.Context, p auth.Principal, ws, invitationID uuid.UUID) error {
	if err := s.az.Can(ctx, p, authz.ActionManageMembers, authz.Workspace(ws)); err != nil {
		return err
	}
	return domainError(s.repo.RevokeInvitation(ctx, ws, invitationID, p.ID))
}

// Accept redeems an invitation token for p. The token is the authorization,
// so there is no authz.Can check; it only works for the email it was sent
// to.
func (s *Service) Accept(ctx context.Context, p auth.Principal, token string) (Joined, error) {
	if p.ID == uuid.Nil {
		return Joined{}, errs.Unauthenticated("authentication required")
	}
	if p.Kind != auth.KindUser {
		return Joined{}, errs.Forbidden("only users can accept invitations")
	}
	if p.Email == "" {
		return Joined{}, errs.Forbidden("your account has no email address to match the invitation")
	}
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, tokenPrefix) {
		return Joined{}, domainError(ErrInvitationNotFound)
	}
	j, err := s.repo.AcceptInvitation(ctx, Acceptance{TokenHash: hashToken(token), UserID: p.ID, Email: p.Email})
	return j, domainError(err)
}

// CheckRoleChange decides whether actor may move a member from target to
// next while the workspace has owners owners. Repositories call it with the
// memberships locked.
func CheckRoleChange(actor, target, next authz.Role, owners int) error {
	if target == next {
		return nil
	}
	if actor != authz.RoleOwner && actor != authz.RoleAdmin {
		return errs.Forbidden("you do not have permission to do this")
	}
	if actor != authz.RoleOwner && (target == authz.RoleOwner || next == authz.RoleOwner) {
		return errs.Forbidden("only owners can change an owner's role or make someone an owner")
	}
	if target == authz.RoleOwner && owners <= 1 {
		return errs.Conflict("a workspace needs at least one owner")
	}
	return nil
}

// CheckRemoval decides whether actor may remove a member with role target
// (themselves when self) while the workspace has owners owners.
func CheckRemoval(actor, target authz.Role, owners int, self bool) error {
	if !self && actor != authz.RoleOwner && actor != authz.RoleAdmin {
		return errs.Forbidden("you do not have permission to do this")
	}
	if !self && actor != authz.RoleOwner && target == authz.RoleOwner {
		return errs.Forbidden("only owners can remove an owner")
	}
	if target == authz.RoleOwner && owners <= 1 {
		return errs.Conflict("a workspace needs at least one owner")
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isRole(r authz.Role) bool {
	switch r {
	case authz.RoleOwner, authz.RoleAdmin, authz.RoleDeveloper, authz.RoleProjectManager:
		return true
	}
	return false
}

func checkEmail(email string) string {
	if email == "" {
		return "required"
	}
	if len(email) > maxEmailLength {
		return "must be at most 254 characters"
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "must be a plain email address"
	}
	return ""
}

func domainError(err error) error {
	switch {
	case errors.Is(err, ErrMemberNotFound):
		return errs.NotFound("member not found")
	case errors.Is(err, ErrInvitationNotFound):
		return errs.NotFound("invitation not found or no longer valid")
	case errors.Is(err, ErrEmailMismatch):
		return errs.Forbidden("this invitation was sent to a different email address")
	case errors.Is(err, ErrAlreadyMember):
		return errs.Conflict("that person is already a member of this workspace")
	case errors.Is(err, ErrInvitationPending):
		return errs.Conflict("there is already an open invitation for that email")
	}
	return err
}
