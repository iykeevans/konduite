package projects

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/authz"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

const (
	maxNameLength        = 100
	maxDescriptionLength = 2000
	maxURLLength         = 500
)

// scp-like git address: user@host:path. The user part cannot hold a
// password because the first colon ends the host.
var scpLikeURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

// Service is the project use cases. Every method takes the caller from the
// verified token, never from the request body.
type Service struct {
	repo Repository
	az   Authorizer
}

// NewService returns a Service storing projects in repo and checking
// permissions with az.
func NewService(repo Repository, az Authorizer) *Service {
	return &Service{repo: repo, az: az}
}

// Create makes a project in workspaceID. The caller becomes its maintainer.
func (s *Service) Create(ctx context.Context, p auth.Principal, workspaceID uuid.UUID, in CreateInput) (Project, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.RepositoryURL = strings.TrimSpace(in.RepositoryURL)
	fields := map[string]string{}
	checkName(fields, in.Name)
	checkDescription(fields, in.Description)
	checkRepositoryURL(fields, in.RepositoryURL)
	if len(fields) > 0 {
		return Project{}, errs.Invalid("invalid project", fields)
	}
	if err := s.az.Can(ctx, p, authz.ActionCreateProject, authz.Workspace(workspaceID)); err != nil {
		return Project{}, err
	}
	project, err := s.repo.Create(ctx, NewProject{
		WorkspaceID: workspaceID, CreatorID: p.ID,
		Name: in.Name, Description: in.Description, RepositoryURL: in.RepositoryURL,
	})
	return project, domainError(err)
}

// Get returns project id if p may view it.
func (s *Service) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (Project, error) {
	if err := s.canView(ctx, p, id); err != nil {
		return Project{}, err
	}
	project, err := s.repo.Get(ctx, id)
	return project, domainError(err)
}

// List returns a page of the projects in workspaceID that p may view.
// Workspace owners and admins see every project; other members see the ones
// they have a project role in. The repository query applies that rule so a
// page is never short of visible rows.
func (s *Service) List(ctx context.Context, p auth.Principal, workspaceID uuid.UUID, in ListInput) (ListPage, error) {
	if in.Limit < 1 {
		return ListPage{}, errs.Invalid("invalid pagination parameters", map[string]string{"limit": "must be at least 1"})
	}
	if err := s.az.Can(ctx, p, authz.ActionViewProjects, authz.Workspace(workspaceID)); err != nil {
		return ListPage{}, err
	}
	items, err := s.repo.List(ctx, workspaceID, p.ID, in.After, in.Limit+1)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Items: items}
	if len(items) > in.Limit {
		page.Items = items[:in.Limit]
		page.Next = page.Items[in.Limit-1].Name
	}
	return page, nil
}

// Update changes the name, description and/or repository URL of project
// id. Maintainers (and workspace owners and admins) may update; other people
// who can view it get 403, everyone else 404.
func (s *Service) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateInput) (Project, error) {
	fields := map[string]string{}
	if in.Name != nil {
		in.Name = trimmed(*in.Name)
		checkName(fields, *in.Name)
	}
	if in.Description != nil {
		in.Description = trimmed(*in.Description)
		checkDescription(fields, *in.Description)
	}
	if in.RepositoryURL != nil {
		in.RepositoryURL = trimmed(*in.RepositoryURL)
		checkRepositoryURL(fields, *in.RepositoryURL)
	}
	if in.Name == nil && in.Description == nil && in.RepositoryURL == nil {
		return Project{}, errs.Invalid("provide a name, description or repository_url to update", nil)
	}
	if len(fields) > 0 {
		return Project{}, errs.Invalid("invalid project", fields)
	}
	if err := s.canView(ctx, p, id); err != nil {
		return Project{}, err
	}
	if err := s.az.Can(ctx, p, authz.ActionUpdateProject, authz.Project(id)); err != nil {
		return Project{}, err
	}
	project, err := s.repo.Update(ctx, id, p.ID, in)
	return project, domainError(err)
}

// canView turns authz's 403 for a workspace member with no project role into
// 404: List hides those projects, so answering 403 here would confirm they
// exist.
func (s *Service) canView(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	err := s.az.Can(ctx, p, authz.ActionViewProject, authz.Project(id))
	if e, ok := errs.As(err); ok && e.Kind == errs.KindForbidden {
		return notFound()
	}
	return err
}

func trimmed(s string) *string {
	s = strings.TrimSpace(s)
	return &s
}

func checkName(fields map[string]string, name string) {
	switch {
	case name == "":
		fields["name"] = "required"
	case utf8.RuneCountInString(name) > maxNameLength:
		fields["name"] = "must be at most 100 characters"
	}
}

func checkDescription(fields map[string]string, description string) {
	if utf8.RuneCountInString(description) > maxDescriptionLength {
		fields["description"] = "must be at most 2000 characters"
	}
}

// checkRepositoryURL accepts https://, ssh:// and scp-like (git@host:path)
// git URLs with no embedded credentials. Messages never repeat the URL,
// since a rejected one may hold a secret.
func checkRepositoryURL(fields map[string]string, raw string) {
	const notGit = "must be an https or ssh git URL"
	switch {
	case raw == "":
		return
	case len(raw) > maxURLLength:
		fields["repository_url"] = "must be at most 500 characters"
		return
	case strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		fields["repository_url"] = notGit
		return
	case scpLikeURL.MatchString(raw):
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		fields["repository_url"] = notGit
		return
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			fields["repository_url"] = "must not include credentials"
			return
		}
	case "ssh":
		if _, hasPassword := u.User.Password(); hasPassword {
			fields["repository_url"] = "must not include credentials"
			return
		}
	default:
		fields["repository_url"] = notGit
		return
	}
	if u.Hostname() == "" || strings.Trim(u.Path, "/") == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		fields["repository_url"] = notGit
	}
}

func notFound() error { return errs.NotFound("project not found") }

func domainError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return notFound()
	case errors.Is(err, ErrNameTaken):
		return errs.Conflict("a project with that name already exists in this workspace")
	}
	return err
}
