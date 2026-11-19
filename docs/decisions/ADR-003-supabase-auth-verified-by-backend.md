# ADR-003: Supabase Auth for users, verified by the backend

- Status: Accepted
- Date: 2026-11-19
- Tickets: PLAT-006

## Context

Developers sign in to the dashboard and the CLI. Building and running our own identity provider isn't where the product's value is, and Supabase already hosts the database (ADR-002). The backend still has to decide for itself who is calling, because it is the only place authorization happens (ADR-010).

## Decision

- Users sign up and sign in with Supabase Auth. The web app uses `@supabase/ssr`.
- The API verifies Supabase access tokens itself against the project's JWKS (`SUPABASE_URL/auth/v1/.well-known/jwks.json`): issuer `SUPABASE_URL/auth/v1`, audience `authenticated`, `sub` a UUID, algorithm ES256 or RS256 only. HS256 is refused.
- Every `/api/v1` route is wrapped by the authentication middleware, which puts an `auth.Principal` in the request context. Handlers read it with `auth.PrincipalFrom(ctx)` and take user ids only from it.
- A bad or expired token gets 401. A failure to fetch the JWKS is a 500, so a Supabase outage doesn't sign everyone out.
- Runtimes and agents will become further principal kinds, not a second authentication path.

## Consequences

- No password storage or session management in our code.
- The API depends on Supabase's JWKS being reachable. Keys are cached for 10 minutes, and an unknown `kid` triggers at most one refetch per minute.
- The legacy anon and service_role keys can never authenticate an API call.
- Details and pitfalls are in `docs/ai/memory/supabase-token-verification.md`.
