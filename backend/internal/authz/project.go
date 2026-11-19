package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

// ProjectRole is a user's role in a project, as stored in project_members.role.
type ProjectRole string

// Project roles.
const (
	ProjectRoleMaintainer  ProjectRole = "maintainer"
	ProjectRoleContributor ProjectRole = "contributor"
	ProjectRoleViewer      ProjectRole = "viewer"
)

// Project-scoped actions. Check them against Project(id).
const (
	ActionViewProject          Action = "project.view"
	ActionUpdateProject        Action = "project.update"
	ActionDeleteProject        Action = "project.delete"
	ActionViewProjectMembers   Action = "project.members.view"
	ActionManageProjectMembers Action = "project.members.manage"
	ActionManageProjectAgents  Action = "project.agents.manage"
	// ActionContribute covers posting messages and creating or changing
	// tasks, blockers and decisions in the project.
	ActionContribute Action = "project.contribute"
)

var projectEveryone = []ProjectRole{ProjectRoleMaintainer, ProjectRoleContributor, ProjectRoleViewer}

var projectAllowed = map[Action][]ProjectRole{
	ActionViewProject:          projectEveryone,
	ActionUpdateProject:        {ProjectRoleMaintainer},
	ActionDeleteProject:        {ProjectRoleMaintainer},
	ActionViewProjectMembers:   projectEveryone,
	ActionManageProjectMembers: {ProjectRoleMaintainer},
	ActionManageProjectAgents:  {ProjectRoleMaintainer},
	ActionContribute:           {ProjectRoleMaintainer, ProjectRoleContributor},
}

// Workspace owners and admins maintain every project in their workspace
// without a project_members row.
var implicitMaintainers = []Role{RoleOwner, RoleAdmin}

// Project returns the resource for project id. Can resolves the project's
// workspace itself, so callers never pair a project with a workspace.
func Project(id uuid.UUID) Resource { return Resource{ProjectID: id} }

// ProjectMembership is a user's standing in a project and its workspace.
// ProjectRole is empty when the user is a workspace member with no
// project_members row.
type ProjectMembership struct {
	WorkspaceID   uuid.UUID
	WorkspaceRole Role
	ProjectRole   ProjectRole
}

// ProjectMemberships looks up a user's standing in a project. It returns
// ErrNotMember when the project does not exist or the user is not a member
// of the project's workspace. A Memberships passed to New that also
// implements ProjectMemberships enables Project resources.
type ProjectMemberships interface {
	ProjectMembership(ctx context.Context, projectID, userID uuid.UUID) (ProjectMembership, error)
}

// ErrNoGrant is returned by a Grants lookup when the principal has no grant
// in the project, including when the project does not exist.
var ErrNoGrant = errors.New("authz: no project grant")

// Grants looks up the project actions a non-user principal (an agent) was
// explicitly granted. It is the hook for agent_project_access.
type Grants interface {
	ProjectActions(ctx context.Context, principalID, projectID uuid.UUID) ([]Action, error)
}

// Option configures an Authorizer.
type Option func(*Authorizer)

// WithGrants authorizes principals of kind on Project resources through g.
// Such principals can do only what g returns; they still cannot see
// Workspace resources.
func WithGrants(kind auth.Kind, g Grants) Option {
	return func(a *Authorizer) { a.grants[kind] = g }
}

func (a *Authorizer) canProject(ctx context.Context, p auth.Principal, action Action, r Resource) error {
	if p.Kind != auth.KindUser {
		return a.canProjectByGrant(ctx, p, action, r)
	}

	projects, ok := a.members.(ProjectMemberships)
	if !ok {
		return errors.New("authz: memberships cannot look up projects")
	}
	m, err := projects.ProjectMembership(ctx, r.ProjectID, p.ID)
	if errors.Is(err, ErrNotMember) {
		return projectNotFound()
	}
	if err != nil {
		return fmt.Errorf("authz: look up project role: %w", err)
	}
	if r.WorkspaceID != uuid.Nil && r.WorkspaceID != m.WorkspaceID {
		return projectNotFound()
	}

	if roles, ok := projectAllowed[action]; ok {
		if slices.Contains(implicitMaintainers, m.WorkspaceRole) || slices.Contains(roles, m.ProjectRole) {
			return nil
		}
		return forbidden()
	}
	if slices.Contains(allowed[action], m.WorkspaceRole) {
		return nil
	}
	return forbidden()
}

func (a *Authorizer) canProjectByGrant(ctx context.Context, p auth.Principal, action Action, r Resource) error {
	g, ok := a.grants[p.Kind]
	if !ok {
		return projectNotFound()
	}
	actions, err := g.ProjectActions(ctx, p.ID, r.ProjectID)
	if errors.Is(err, ErrNoGrant) {
		return projectNotFound()
	}
	if err != nil {
		return fmt.Errorf("authz: look up project grant: %w", err)
	}
	if slices.Contains(actions, action) {
		return nil
	}
	return forbidden()
}

func projectNotFound() error { return errs.NotFound("project not found") }
