---
name: runtime-cli-auth
description: "How agenthub login/logout work and what is stored where: read before touching runtime/internal/auth, security, or the runtime revoke endpoint"
metadata:
  type: project
  modified: 2026-11-20
---

# Runtime CLI authentication

Decided in RT-002 (#41), 2026-11-19. Code: `runtime/internal/auth`, `runtime/internal/security`, `runtime/internal/cli/login.go`, `backend/internal/runtimes` (revoke).

- `agenthub login` runs Supabase PKCE through a one-shot listener on `127.0.0.1:<random port>/callback?state=<random>`: OAuth (`--provider`, `/auth/v1/authorize`) or magic link (`--email`, `/auth/v1/otp` with `create_user: false`). The code is exchanged at `/auth/v1/token?grant_type=pkce` with the publishable key in the `apikey` header.
- The Supabase session is used once, to call `POST /api/v1/runtimes/register`, and then dropped. **Only the runtime refresh credential is stored**, in the OS keychain under service `konduite-agenthub`, account = `server_url` without a trailing slash. Storing the user session would let a stolen keychain item act as the user across the whole API.
- Config holds only non-secret values: `supabase_url`, `supabase_publishable_key` (an `sb_secret_` key is refused as `ErrSecret`), `auth_provider`, `workspace_id`, `runtime_id`.
- No keychain (headless Linux without Secret Service) fails with `security.ErrUnavailable` before the browser opens. There is no file fallback; ADR-008 leaves that open.
- If the keychain write fails after registering, login revokes the new credential so it isn't orphaned.
- `agenthub logout` calls `POST /v1/runtime/revoke` (credential in the body, like `/v1/runtime/token`; deletes the credential row, 204), then deletes the keychain item **even if the revoke failed**, and exits non-zero saying so. A 401 from revoke means already revoked. Access tokens issued before the revoke stay valid up to 15 minutes until SEC-005.
- `agenthub status` (RT-003, #42, 2026-11-20) prints the local config, whether a credential is stored, and the server's view: it exchanges the credential at `/v1/runtime/token` and calls `GET /v1/runtime/self` with the runtime access token. An unreachable server or rejected credential is printed, not an error. `whoami` stays as the offline-only view.
- **Why `/v1/runtime/self` and not runtime tokens on `GET /api/v1/runtimes/{id}`:** accepting runtime JWTs there would mean a second verifier in the `/api/v1` auth chain, making runtime tokens principals on every user route and teaching `authz.Can` a runtime kind. `self` is one read-only route whose only resource is the token's own runtime, so the token is the authorization and there is no `authz.Can` call. Revisit when SEC-005/RT-004 give runtimes a real principal kind.
- A wrong `state` on the callback gets 400 and the login keeps waiting, so a forged request can't abort or hijack it.

**How to apply:** for a hosted Supabase project, add `http://127.0.0.1:*/**` to the Auth redirect allow-list; local Supabase's `site_url` is on 127.0.0.1, which GoTrue's same-host rule should accept (not yet verified against a running stack). If the redirect isn't allowed, GoTrue silently sends the browser to `site_url` and login times out. Tests fake Supabase and the API with `runtime/internal/auth/authtest`; they never hit the real flow.

Related: [[supabase-token-verification]]
