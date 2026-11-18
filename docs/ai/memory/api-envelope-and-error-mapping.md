---
name: api-envelope-and-error-mapping
description: "How every API response and error is shaped, and why: load before writing any HTTP handler"
metadata:
  type: project
  modified: 2026-11-18
---

# API envelope and error mapping

Decided in PLAT-002 (#12), 2026-11-18. Every handler from PLAT-007 on builds on it.

- **Success:** `{"data": ...}`. List endpoints add `"meta": {"next_cursor", "has_more"}` through `httpx.JSONPage`. `next_cursor` is omitted on the last page.
- **Error:** `{"error": {"code", "message", "details"}}`.
- **How handlers fail:** return `errs.NotFound`, `Invalid(msg, fields)`, `Unauthenticated`, `Forbidden` or `Conflict` (`backend/internal/platform/errs`), then call `httpx.Error`. Only `httpx/error.go` maps a kind to a status (404, 422, 401, 403, 409). Handlers never pick a status code for an error themselves.
- **Unknown errors:** any error that isn't an `errs.Error` becomes a generic 500 `internal` / "internal server error". It is logged, and its text never reaches the client. Panics get the same treatment (`internal/api/recover.go`).
- **Pagination:** `limit` is 1-100 with a default of 50. Out-of-range values return **422; they are not clamped**. Cursors are opaque base64url JSON (`httpx.EncodeCursor`, `Page.DecodeCursor`). Callers must not parse them.
- **Readiness:** checks are injected as named funcs in `api.Deps.ReadinessChecks`. `/readyz` returns only the names of failing checks, never their error text.
- **Routes:** domains mount their routes under `/api/v1` through `api.Deps.Routes`.

**Why:** a single place for status mapping keeps errors consistent across about 15 domains and makes it impossible to leak internal error text such as SQL errors or panic values. Rejecting an oversized `limit` was chosen over silently capping it. A client asking for 500 items and quietly getting 100 would page incorrectly. The router takes readiness checks as funcs so it never depends on Postgres or NATS.

**How to apply:** in a new handler, return `errs.*` for expected failures and pass anything else to `httpx.Error` unchanged. Don't call `httpx.Fail` with a hand-picked status except for transport-level cases like 404 for an unknown route or 503 for readiness. Validation problems go in `errs.Invalid` with per-field messages. If you add a new error kind, add it to `statusByKind`, the table test in `httpx/error_test.go`, and the `code` enum in `docs/api/openapi.yaml`.
