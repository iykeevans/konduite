# Shared project memory

Read this index at task startup, then load the notes relevant to the work. Project instructions live in [AGENTS.md](../../../AGENTS.md).

- [API envelope and error mapping](api-envelope-and-error-mapping.md) — response/error shapes, errs kinds → status in httpx only, limit 422 not clamp, opaque cursors.
- [User ids have no FK to auth.users](user-ids-have-no-auth-fk.md) — user id columns are plain uuids so migrations run on vanilla Postgres; take ids only from the JWT principal.
