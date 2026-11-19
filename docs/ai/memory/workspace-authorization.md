---
name: workspace-authorization
description: "How authz.Can decides, which errors it returns and why non-members get 404: read before writing any handler or worker that touches workspace data"
metadata:
  type: project
  modified: 2026-11-19
---

# Workspace authorization

Decided in SEC-001 (#24), 2026-11-19. Code: `backend/internal/authz`.

- **Entry point:** `authz.New(authz.StoreMemberships(store.New(pool)))` builds an `*Authorizer`; every handler and worker calls `az.Can(ctx, principal, action, authz.Workspace(id))` before touching workspace data. The package lives in `internal/authz`, not `internal/auth` as the ticket text said, so the call site reads `authz.Can` as AGENTS.md mandates.
- **Errors:** non-member, missing workspace and non-user principal all return the same `errs.NotFound("workspace not found")` (404). A member without the role gets `errs.Forbidden` (403). An empty principal is `Unauthenticated`. A failed lookup or a `Resource` with no workspace id is a plain error, so httpx turns it into a 500.
- **Deny by default:** an action missing from the `allowed` table, or a role string the code doesn't know, is 403.
- **Role matrix** (the `allowed` map in `authz.go`, mirrored in `TestCanRoleByAction`): everyone views the workspace, members and projects; owner+admin update the workspace, manage members and read the audit log; only owner deletes; owner+admin+project_manager create projects; owner+admin+developer register runtimes.

**Why 404 for non-members:** a 403 would confirm the workspace exists, letting anyone probe ids. One message for every not-visible case keeps that true even if a handler logs or echoes it.

**How to apply:**
- Build the `Resource` from server-owned data: the workspace id in the path, or the stored row's `workspace_id`. Never from a request body or message content.
- Pass the error from `Can` to `httpx.Error` unchanged.
- A new action needs a constant, an `allowed` entry and a row in `TestCanRoleByAction`.
- Project-level checks (SEC-002) and runtime/agent principals (SEC-003, SEC-004) extend `Resource` and the principal `Kind` switch; non-user kinds are denied with 404 until then.

Related: [[api-envelope-and-error-mapping]], [[user-ids-have-no-auth-fk]]
