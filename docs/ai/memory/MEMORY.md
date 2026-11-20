# Shared project memory

Read this index at task startup, then load the notes relevant to the work. Project instructions live in [AGENTS.md](../../../AGENTS.md).

- [API envelope and error mapping](api-envelope-and-error-mapping.md) — response/error shapes, errs kinds → status in httpx only, limit 422 not clamp, opaque cursors.
- [User ids have no FK to auth.users](user-ids-have-no-auth-fk.md) — user id columns are plain uuids so migrations run on vanilla Postgres; take ids only from the JWT principal.
- [Supabase token verification](supabase-token-verification.md) — JWKS/ES256 checks, outage is 500 not 401, why /api/v1 is wrapped not `Use`d.
- [Workspace and project authorization](workspace-authorization.md) — authz.Can is the one check; non-members get 404 not 403; role x action matrices; owner/admin maintain every project; agent grants hook; build Resource from server-owned ids; project conversations follow the project (list filters in SQL); production wiring uses NewWithProjects.
- [Domain handler pattern](domain-handler-pattern.md) — handler/service/repository split (workspaces is the reference), routes by full path not r.Route, audit rows in the write tx.
- [Agent identity](agent-identity.md) — agents keyed by (runtime_id, provider, name) from runtime.hello; unannounced go offline not deleted; composite workspace FKs.
- [Runtime CLI authentication](runtime-cli-auth.md) — agenthub login/logout: loopback PKCE, only the runtime credential in the keychain, logout revokes then clears even on failure.
- [Message ordering and idempotency](message-ordering-and-idempotency.md) — seq from conversations.last_seq in the write tx; AppendTx returns the original on retry without using a number; message.created has ids, not content; POST/GET message API: Changeable authz, sender auto-joined, Idempotency-Key header, 200 on retry.
- [Parallel agent worktrees](parallel-agent-worktrees.md) — fresh worktree needs `next typegen` before the web type-check (LayoutProps); rerun an integration package alone when testcontainers miss their deadline.
- [Workspace membership](workspace-membership.md) — invitation tokens hashed and shown once, accept link in a URL fragment POSTed in a body, email-bound accept, last-owner guard under row locks.
- [Consumer retry and dead-letter](consumer-retry-and-dlq.md) — every consumer uses nats.Client.Consume; publish DLQ then Term; replay gets a new msg id; admin allow-list gate.
