package projectmembers

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

// Service is the project membership use cases. Every method takes the
// caller from the verified token, never from the request body.
type Service struct {
	repo Repository
	az   Authorizer
}

// NewService returns a Service storing memberships in repo and checking
// permissions with az.
func NewService(repo Repository, az Authorizer) *Service {
	return &Service{repo: repo, az: az}
}

// List returns a page of project projectID's members to anyone who can view
// the project.
func (s *Service) List(ctx context.Context, p auth.Principal, projectID uuid.UUID, after Cursor, limit int) (Page, error) {
	if limit < 1 {
		return Page{}, errs.Invalid("invalid pagination parameters", map[string]string{"limit": "must be at least 1"})
	}
	if err := s.canView(ctx, p, projectID); err != nil {
		return Page{}, err
	}
	if err := s.az.Can(ctx, p, authz.ActionViewProjectMembers, authz.Project(projectID)); err != nil {
		return Page{}, err
	}
	items, err := s.repo.List(ctx, projectID, after, limit+1)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &Cursor{JoinedAt: last.JoinedAt, UserID: last.UserID}
	}
	return page, nil
}

// Add gives userID, who must already be in the project's workspace, a role
// in the project. Maintainers and workspace owners and admins may.
func (s *Service) Add(ctx context.Context, p auth.Principal, projectID, userID uuid.UUID, role string) (Member, error) {
	next := authz.ProjectRole(strings.TrimSpace(role))
	fields := map[string]string{}
	if userID == uuid.Nil {
		fields["user_id"] = "required"
	}
	if !isRole(next) {
		fields["role"] = roleMessage
	}
	if len(fields) > 0 {
		return Member{}, errs.Invalid("invalid project member", fields)
	}
	if err := s.canManage(ctx, p, projectID); err != nil {
		return Member{}, err
	}
	m, err := s.repo.Add(ctx, Change{ProjectID: projectID, ActorID: p.ID, UserID: userID, Role: next})
	return m, domainError(err)
}

// ChangeRole gives member userID a new project role, within
// CheckMaintainerChange's limits.
func (s *Service) ChangeRole(ctx context.Context, p auth.Principal, projectID, userID uuid.UUID, role string) (Member, error) {
	next := authz.ProjectRole(strings.TrimSpace(role))
	if !isRole(next) {
		return Member{}, errs.Invalid("invalid role", map[string]string{"role": roleMessage})
	}
	if err := s.canManage(ctx, p, projectID); err != nil {
		return Member{}, err
	}
	m, err := s.repo.ChangeRole(ctx, Change{ProjectID: projectID, ActorID: p.ID, UserID: userID, Role: next})
	return m, domainError(err)
}

// Remove takes userID out of the project, within CheckMaintainerChange's
// limits. Maintainers and workspace owners and admins may remove anyone;
// any member may leave.
func (s *Service) Remove(ctx context.Context, p auth.Principal, projectID, userID uuid.UUID) error {
	var err error
	if userID == p.ID {
		err = s.canView(ctx, p, projectID)
	} else {
		err = s.canManage(ctx, p, projectID)
	}
	if err != nil {
		return err
	}
	return domainError(s.repo.Remove(ctx, Change{ProjectID: projectID, ActorID: p.ID, UserID: userID}))
}

// CheckMaintainerChange decides whether a member with role target may be
// given role next (empty when they are being removed) while the project has
// maintainers maintainers. Workspace owners and admins maintain every
// project implicitly, so actorImplicit lets them take the last explicit
// maintainer away: the project stays managed by them. Anyone else would
// leave it with no maintainer of its own. Repositories call it with the
// memberships locked.
func CheckMaintainerChange(actorImplicit bool, target, next authz.ProjectRole, maintainers int) error {
	if target != authz.ProjectRoleMaintainer || next == authz.ProjectRoleMaintainer {
		return nil
	}
	if maintainers <= 1 && !actorImplicit {
		return errs.Conflict("a project needs at least one maintainer; make someone else a maintainer first")
	}
	return nil
}

// canView turns authz's 403 for a workspace member with no project role into
// 404, as the projects package does: the project list hides those projects.
func (s *Service) canView(ctx context.Context, p auth.Principal, projectID uuid.UUID) error {
	err := s.az.Can(ctx, p, authz.ActionViewProject, authz.Project(projectID))
	if e, ok := errs.As(err); ok && e.Kind == errs.KindForbidden {
		return errs.NotFound("project not found")
	}
	return err
}

func (s *Service) canManage(ctx context.Context, p auth.Principal, projectID uuid.UUID) error {
	if err := s.canView(ctx, p, projectID); err != nil {
		return err
	}
	return s.az.Can(ctx, p, authz.ActionManageProjectMembers, authz.Project(projectID))
}

const roleMessage = "must be maintainer, contributor or viewer"

func isRole(r authz.ProjectRole) bool {
	switch r {
	case authz.ProjectRoleMaintainer, authz.ProjectRoleContributor, authz.ProjectRoleViewer:
		return true
	}
	return false
}

func domainError(err error) error {
	switch {
	case errors.Is(err, ErrMemberNotFound):
		return errs.NotFound("project member not found")
	case errors.Is(err, ErrAlreadyMember):
		return errs.Conflict("that person is already a member of this project")
	case errors.Is(err, ErrNotWorkspaceMember):
		return errs.Invalid("invalid project member", map[string]string{"user_id": "must be a member of the project's workspace"})
	}
	return err
}
