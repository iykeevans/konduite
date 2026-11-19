---
name: supabase-token-verification
description: "How the API authenticates users (Supabase JWT via JWKS) and why outages are 500 not 401: read before touching auth, /api/v1 mounting or adding a principal kind"
metadata:
  type: project
  modified: 2026-11-18
---

# Supabase token verification

Decided in PLAT-006 (#16), 2026-11-18. Code: `backend/internal/auth` (`verifier.go`, `middleware.go`, `principal.go`).

- The API verifies Supabase access tokens itself against `SUPABASE_URL/auth/v1/.well-known/jwks.json`. Issuer is `SUPABASE_URL/auth/v1` and audience is `authenticated`; `sub` must be a UUID. Local Supabase CLI (2.122) signs with **ES256**. Only ES256 and RS256 are accepted. HS256 is refused on purpose, because accepting it with a public JWKS lets anyone forge tokens. The legacy anon and service_role keys are HS256, so they are always 401.
- Keys are fetched lazily and kept for 10 minutes. A token with an unknown `kid` triggers at most one refetch per minute (`MinRefresh`), so key rotation is picked up quickly while random kids can't flood Supabase. If a refetch fails, the old keys keep being used.
- Handlers read the caller with `auth.PrincipalFrom(ctx)`. It is `{Kind: user, ID}` today; roles come from SEC-001.

**Why outages are 500:** `Verify` returns `ErrInvalidToken` only for bad tokens. A JWKS fetch failure is a different error, and the middleware hands it to `httpx.Error` (generic 500). A 401 tells the web app the session is dead, so a Supabase blip would sign everyone out.

**Why `/api/v1` is mounted as `authenticate(v1)`:** chi skips a subrouter's `Use` middleware while the subrouter has no routes. With `v1.Use(auth)` and no domain routes, `/api/v1/*` was reachable without a token. This was caught by running the binary, not by unit tests. `api.Deps.Authenticate` == nil denies everything. Don't "simplify" this back to `v1.Use`.

**How to apply:** new domain routes go in `api.Deps.Routes` and are authenticated automatically. Never take a user id from a request body ([[user-ids-have-no-auth-fk]]). When agents and runtimes become principals (RT-002, SEC-004), add a `Kind` rather than a second middleware path.

Related: [[api-envelope-and-error-mapping]]
