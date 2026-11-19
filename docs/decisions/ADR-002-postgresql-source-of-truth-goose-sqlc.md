# ADR-002: PostgreSQL as the source of truth, with goose migrations and sqlc queries

- Status: Accepted
- Date: 2026-11-19
- Tickets: PLAT-004, PLAT-005

## Context

Messages, deliveries and executions must survive crashes, keep per-conversation order and be deduplicated by unique constraints. The event bus (ADR-005) distributes changes but can't be the record of them. The schema also has to evolve under version control, and handwritten SQL mapping code is a common source of bugs.

## Decision

- PostgreSQL is the source of truth for all platform state. Locally it runs through `supabase start`. Hosted environments use the Supabase project's database.
- Access goes through a pgx/v5 pool. Multi-statement writes use `db.WithTx(ctx, beginner, fn)`, which commits when `fn` returns nil and rolls back otherwise.
- Schema changes are goose migrations in `backend/migrations`, applied with `make migrate CMD=up|down|status`.
- Queries live in `backend/sql/queries` and sqlc compiles them into `backend/internal/store`. CI fails if the generated code is stale (`make sqlc-check`).
- Tables use UUID primary keys, foreign keys between our own tables, uniqueness constraints for idempotency and tenant indexes.
- User id columns are plain `uuid` with no foreign key to Supabase's `auth.users`, so migrations and tests run on vanilla Postgres (see `docs/ai/memory/user-ids-have-no-auth-fk.md`).

## Consequences

- The database enforces the correctness guarantees (ordering, dedupe, durable before acknowledged), not application memory.
- Integration tests run against a real Postgres container through testcontainers, never against a shared environment.
- Every query change needs `make sqlc` and a committed regeneration.
- Removing data when a Supabase user is deleted is an application job, because the database can't cascade from `auth.users`.
