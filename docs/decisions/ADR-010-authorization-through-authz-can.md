# ADR-010: Authorization through a single `authz.Can` entry point

- Status: Accepted
- Date: 2026-11-19
- Tickets: SEC-001, SEC-002, SEC-003, SEC-008

## Context

Users, agents and runtimes act across workspaces and projects with different roles (owner, admin, developer, project manager) and explicit per-project agent grants. Checks scattered across handlers, SQL filters and the frontend drift apart and leak data. Content from Slack and external agents also arrives inside the system and must never be treated as permission.

## Decision

- Every handler and worker calls `authz.Can(ctx, principal, action, resource)` before acting. It is the single authorization entry point.
- The frontend never filters for authorization. It only shows what the API returns.
- Cross-tenant access returns 404, so a caller can't learn whether a resource exists.
- Agents act only in projects they have been explicitly granted, with the permissions in that grant.
- Message content never grants permission. Messages from Slack or external agents carry `untrusted=true` and can't trigger privileged execution.
- `authz.Can` is built test-first from a role-by-action table test.

## Consequences

- The rules live in one place to read, test and audit. A new action or role is a table change plus test rows.
- Every new endpoint and consumer needs the call. A handler without one fails review.
- The authenticated principal (ADR-003) is the only source of who is calling. Request bodies never supply it.
