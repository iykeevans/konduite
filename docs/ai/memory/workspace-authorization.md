---
name: workspace-authorization
description: "How authz.Can decides for workspaces and projects, how workspace and project roles combine, the agent grants hook, and why non-members get 404: read before writing any handler or worker that touches workspace or project data"
metadata:
  type: project
  modified: 2026-11-20
---

# Workspace and project authorization

Decided in SEC-001 (#24) and SEC-002 (#25), 2026-11-19. Code: `backend/internal/authz`.

- **Entry point:** `authz.NewWithProjects(authz.StoreMemberships(store.New(pool)))` builds an `*Authorizer`; every handler and worker calls `az.Can(ctx, principal, action, authz.Workspace(id))` before touching workspace data. The package lives in `internal/authz`, not `internal/auth` as the ticket text said, so the call site reads `authz.Can` as AGENTS.md mandates.
- **Errors:** non-member, missing workspace and non-user principal all return the same `errs.NotFound("workspace not found")` (404). A member without the role gets `errs.Forbidden` (403). An empty principal is `Unauthenticated`. A failed lookup or a `Resource` with no workspace id is a plain error, so httpx turns it into a 500.
- **Deny by default:** an action missing from the `allowed` table, or a role string the code doesn't know, is 403.
- **Role matrix** (the `allowed` map in `authz.go`, mirrored in `TestCanRoleByAction`): everyone views the workspace, members and projects; owner+admin update the workspace, manage members and read the audit log; only owner deletes; owner+admin+project_manager create projects; owner+admin+developer register runtimes.

**Why 404 for non-members:** a 403 would confirm the workspace exists, letting anyone probe ids. One message for every not-visible case keeps that true even if a handler logs or echoes it.

**How to apply:**
- Build the `Resource` from server-owned data: the workspace id in the path, or the stored row's `workspace_id`. Never from a request body or message content.
- Pass the error from `Can` to `httpx.Error` unchanged.
- A new action needs a constant, an `allowed` entry and a row in `TestCanRoleByAction`.
- A new project action needs a constant, a `projectAllowed` entry in `project.go` and a column row in `TestCanProjectRoleByAction`.

## Projects (SEC-002)

- **Resource:** `authz.Project(projectID)`. `Can` resolves the project's workspace itself (one query joining `projects`, `workspace_members`, `project_members`), so handlers on `/projects/{id}` routes need no extra lookup and can't pair a project with the wrong workspace. A `Resource` with both ids set must match, else 404.
- **Role combination:** workspace owner/admin implicitly maintain every project. Everyone else needs a `project_members` row (maintainer > contributor > viewer). A workspace member with no project row gets 403, not 404, because every member can already list projects. A project member who is no longer in the workspace gets 404: workspace membership is required.
- **Matrix** (`projectAllowed`): all project roles view the project and its members; maintainer+contributor `project.contribute` (messages, tasks, blockers, decisions); only maintainer updates, deletes, manages members and manages agent access. Workspace actions checked on a `Project` resource use the workspace role; project actions on a `Workspace` resource are 403.
- **Errors:** every not-visible project case returns `errs.NotFound("project not found")`.
- **Wiring (explicit since 2026-11-20):** production builds the authorizer with `authz.NewWithProjects(authz.StoreMemberships(q))` (`newAuthorizer` in `cmd/api/main.go`); `NewWithProjects` takes a `Directory` (Memberships + ProjectMemberships), so dropping the project lookup is a compile error, and `cmd/api/main_test.go` checks the wiring. `authz.New(m)` still works for workspace-only test fakes and type-asserts `m` for project lookups; without one, project checks return `authz.ErrNoProjectLookup` (a 500).
- **Agent hook (for SEC-003):** `authz.New(m, authz.WithGrants(kind, g))` routes principals of `kind` on `Project` resources to `g.ProjectActions(ctx, principalID, projectID)`; return `authz.ErrNoGrant` for no grant (404). The principal may do exactly the returned actions (else 403) and still can't see `Workspace` resources. Kinds without a registered `Grants` stay 404. SEC-003 registers the agent kind backed by `agent_project_access`.

## Conversations in a project (2026-11-20)

- A conversation with a `project_id` follows the project, not the workspace: reading needs `project.view`, creating needs `project.contribute` (viewers are read-only, as for messages). Workspace-wide conversations stay open to every member.
- Get of a hidden one returns the same `conversation not found` 404 as a missing one, including when authz said 403.
- The list filters in SQL (`ListConversations`: owner/admin or any `project_members` row), not by calling `Can` per row. That duplicates the `project.view` rule, so `TestPostgresRepositoryListsProjectConversationsOnlyToWhoeverAuthzLetsViewTheProject` cross-checks the SQL against `authz.Can` for every role combination; update both together.
- **Participants** (`POST /conversations/{id}/participants`, `DELETE .../{type}/{participant_id}`) need the right to contribute to the conversation: any member for a workspace-wide one (`workspace.conversations.create`), `project.contribute` for a project one. A user participant must pass `Can` for reading the conversation; agents and runtimes must be in its workspace. Both calls are idempotent (re-add is 200, removing a non-participant is 204).

Related: [[api-envelope-and-error-mapping]], [[user-ids-have-no-auth-fk]]
