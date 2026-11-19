// Package authz decides whether a principal may perform an action on a
// resource. Authorizer.Can is the single entry point; handlers and workers
// never check roles or membership themselves.
//
// The contract callers must meet:
//
//   - Call Can before reading or changing anything in the workspace, and
//     before revealing whether the target exists.
//   - Build the Resource from data the server owns: the workspace id in the
//     URL for workspace routes, or the workspace_id of the stored row for
//     anything inside a workspace. Never from a request body or message
//     content.
//   - Pass the error to httpx.Error unchanged. A non-member gets the same 404
//     as a missing workspace, so the response never reveals that a workspace
//     exists. A member without the role gets 403. A failed lookup is a 500.
//   - When the workspace check passes but the row itself is missing, return
//     errs.NotFound as usual.
package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/iykeevans/konduite/backend/internal/auth"
	"github.com/iykeevans/konduite/backend/internal/platform/errs"
)

// Role is a user's role in a workspace, as stored in workspace_members.role.
type Role string

// Workspace roles.
const (
	RoleOwner          Role = "owner"
	RoleAdmin          Role = "admin"
	RoleDeveloper      Role = "developer"
	RoleProjectManager Role = "project_manager"
)

// Action is something a principal wants to do.
type Action string

// Workspace-scoped actions.
const (
	ActionViewWorkspace   Action = "workspace.view"
	ActionUpdateWorkspace Action = "workspace.update"
	ActionDeleteWorkspace Action = "workspace.delete"
	ActionViewMembers     Action = "workspace.members.view"
	ActionManageMembers   Action = "workspace.members.manage"
	ActionViewProjects    Action = "workspace.projects.view"
	ActionCreateProject   Action = "workspace.projects.create"
	ActionRegisterRuntime Action = "workspace.runtimes.register"
	ActionViewAuditLog    Action = "workspace.audit_log.view"

	ActionViewConversations  Action = "workspace.conversations.view"
	ActionCreateConversation Action = "workspace.conversations.create"
)

var everyone = []Role{RoleOwner, RoleAdmin, RoleDeveloper, RoleProjectManager}

var allowed = map[Action][]Role{
	ActionViewWorkspace:   everyone,
	ActionUpdateWorkspace: {RoleOwner, RoleAdmin},
	ActionDeleteWorkspace: {RoleOwner},
	ActionViewMembers:     everyone,
	ActionManageMembers:   {RoleOwner, RoleAdmin},
	ActionViewProjects:    everyone,
	ActionCreateProject:   {RoleOwner, RoleAdmin, RoleProjectManager},
	ActionRegisterRuntime: {RoleOwner, RoleAdmin, RoleDeveloper},
	ActionViewAuditLog:    {RoleOwner, RoleAdmin},

	ActionViewConversations:  everyone,
	ActionCreateConversation: everyone,
}

// Resource is what an action targets. Build it with Workspace.
type Resource struct {
	WorkspaceID uuid.UUID
}

// Workspace returns the resource for workspace id.
func Workspace(id uuid.UUID) Resource { return Resource{WorkspaceID: id} }

// ErrNotMember is returned by a Memberships lookup when the user has no role
// in the workspace, including when the workspace does not exist.
var ErrNotMember = errors.New("authz: not a workspace member")

// Memberships looks up a user's role in a workspace.
type Memberships interface {
	Role(ctx context.Context, workspaceID, userID uuid.UUID) (Role, error)
}

// Authorizer answers authorization questions. It is safe for concurrent use.
type Authorizer struct {
	members Memberships
}

// New returns an Authorizer that reads roles from members.
func New(members Memberships) *Authorizer {
	return &Authorizer{members: members}
}

// Can returns nil if p may perform action on r. Otherwise it returns
// errs.NotFound when p cannot see the workspace, errs.Forbidden when p can
// see it but lacks the role, errs.Unauthenticated for an empty principal, or
// a plain error (a 500) when the lookup fails or r has no workspace.
func (a *Authorizer) Can(ctx context.Context, p auth.Principal, action Action, r Resource) error {
	if r.WorkspaceID == uuid.Nil {
		return errors.New("authz: resource has no workspace id")
	}
	if p.ID == uuid.Nil {
		return errs.Unauthenticated("authentication required")
	}
	if p.Kind != auth.KindUser {
		return notFound()
	}

	role, err := a.members.Role(ctx, r.WorkspaceID, p.ID)
	if errors.Is(err, ErrNotMember) {
		return notFound()
	}
	if err != nil {
		return fmt.Errorf("authz: look up workspace role: %w", err)
	}

	for _, ok := range allowed[action] {
		if role == ok {
			return nil
		}
	}
	return errs.Forbidden("you do not have permission to do this")
}

func notFound() error { return errs.NotFound("workspace not found") }
