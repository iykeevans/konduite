package workspaces

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	maxNameLength = 100
	minSlugLength = 3
	maxSlugLength = 50
)

// Same pattern as the CHECK on workspaces.slug.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Service is the workspace use cases. Every method takes the caller from
// the verified token, never from the request body.
type Service struct {
	repo Repository
	az   Authorizer
}

// NewService returns a Service storing workspaces in repo and checking
// permissions with az.
func NewService(repo Repository, az Authorizer) *Service {
	return &Service{repo: repo, az: az}
}

// Create makes a workspace owned by p. There is no workspace to check
// permissions against yet, so any signed-in user may create one.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Workspace, error) {
	if err := requireUser(p); err != nil {
		return Workspace{}, err
	}
	name, slug := strings.TrimSpace(in.Name), strings.TrimSpace(in.Slug)
	fields := map[string]string{}
	checkName(fields, name)
	checkSlug(fields, slug)
	if len(fields) > 0 {
		return Workspace{}, errs.Invalid("invalid workspace", fields)
	}
	ws, err := s.repo.Create(ctx, NewWorkspace{Name: name, Slug: slug, OwnerID: p.ID})
	return ws, domainError(err)
}

// Get returns workspace id if p is a member of it.
func (s *Service) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Workspace, error) {
	if err := s.az.Can(ctx, p, authz.ActionViewWorkspace, authz.Workspace(id)); err != nil {
		return Workspace{}, err
	}
	ws, err := s.repo.Get(ctx, id, p.ID)
	return ws, domainError(err)
}

// List returns a page of the workspaces p is a member of. The membership
// join is the authorization: there is no single workspace to pass to Can.
func (s *Service) List(ctx context.Context, p auth.Principal, in ListInput) (ListPage, error) {
	if err := requireUser(p); err != nil {
		return ListPage{}, err
	}
	if in.Limit < 1 {
		return ListPage{}, errs.Invalid("invalid pagination parameters", map[string]string{"limit": "must be at least 1"})
	}
	items, err := s.repo.List(ctx, p.ID, in.After, in.Limit+1)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Items: items}
	if len(items) > in.Limit {
		page.Items = items[:in.Limit]
		page.Next = page.Items[in.Limit-1].Slug
	}
	return page, nil
}

// Update changes the name and/or slug of workspace id. Owners and admins
// may update; other members get 403 and non-members 404.
func (s *Service) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateInput) (Workspace, error) {
	fields := map[string]string{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		checkName(fields, name)
		in.Name = &name
	}
	if in.Slug != nil {
		slug := strings.TrimSpace(*in.Slug)
		checkSlug(fields, slug)
		in.Slug = &slug
	}
	if in.Name == nil && in.Slug == nil {
		return Workspace{}, errs.Invalid("provide a name or slug to update", nil)
	}
	if len(fields) > 0 {
		return Workspace{}, errs.Invalid("invalid workspace", fields)
	}
	if err := s.az.Can(ctx, p, authz.ActionUpdateWorkspace, authz.Workspace(id)); err != nil {
		return Workspace{}, err
	}
	ws, err := s.repo.Update(ctx, id, p.ID, in)
	return ws, domainError(err)
}

func requireUser(p auth.Principal) error {
	if p.ID == uuid.Nil {
		return errs.Unauthenticated("authentication required")
	}
	if p.Kind != auth.KindUser {
		return errs.Forbidden("only users can do this")
	}
	return nil
}

func checkName(fields map[string]string, name string) {
	switch {
	case name == "":
		fields["name"] = "required"
	case utf8.RuneCountInString(name) > maxNameLength:
		fields["name"] = "must be at most 100 characters"
	}
}

func checkSlug(fields map[string]string, slug string) {
	switch {
	case slug == "":
		fields["slug"] = "required"
	case len(slug) < minSlugLength || len(slug) > maxSlugLength:
		fields["slug"] = "must be 3 to 50 characters"
	case !slugPattern.MatchString(slug):
		fields["slug"] = "must be lowercase letters, digits and single hyphens, not starting or ending with a hyphen"
	}
}

func domainError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return errs.NotFound("workspace not found")
	case errors.Is(err, ErrSlugTaken):
		return errs.Conflict("that slug is already taken")
	}
	return err
}
