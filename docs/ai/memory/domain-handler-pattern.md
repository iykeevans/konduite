---
name: domain-handler-pattern
description: "The layout every /api/v1 domain package copies (workspaces is the reference): read before adding a new domain's handler, service or repository"
metadata:
  type: project
  modified: 2026-11-19
---

# Domain handler pattern

Set in PLAT-007 (#17), 2026-11-19. Reference: `backend/internal/workspaces`.

- **Layers:** `handler.go` only decodes, reads the principal and maps to/from JSON. `service.go` validates (`errs.Invalid` with per-field messages), calls `authz.Can`, then the repository. `repository.go` is the only file that touches `store`/pgx, and it returns package sentinels (`ErrNotFound`, `ErrSlugTaken`) that the service turns into `errs.*`. `types.go` holds the `Repository` interface.
- **Service tests use the real authorizer:** the fake repository also implements `authz.Memberships`, so `authz.New(fake)` exercises the real role matrix instead of a stub.
- **Routes are registered by full path** (`r.Get("/workspaces/{workspaceID}", ...)`), never `r.Route("/workspaces", ...)`. chi panics when two domains both mount a subrouter on the same prefix, and projects/members will live under `/workspaces/{workspaceID}/...`.
- **Audit entries are written by the repository in the write's `db.WithTx`**, with `actor_type 'user'`, `action '<resource>.<verb>'`. An update records `{"changes": {field: {from, to}}}` and writes nothing at all when no value changed.
- **A malformed path id is 404**, the same as a missing or hidden one.
- **Create/list have no workspace for `authz.Can`:** create is allowed for any user principal; list authorizes through the membership join. Both reject non-user principals.
- **List cursors** are `httpx.EncodeCursor` of a handler-private struct (`{"after": <last slug>}`); the service asks the repository for `limit+1` rows to know whether there is a next page.
- **Web:** server code calls the API through `apps/web/lib/api/client.ts`, which forwards the Supabase access token and redirects to /login on 401. `KONDUITE_API_URL` is server-only.

Related: [[api-envelope-and-error-mapping]], [[workspace-authorization]]
