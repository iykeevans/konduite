---
name: user-ids-have-no-auth-fk
description: "Why user id columns are plain uuids with no foreign key to Supabase auth.users: read before adding a table that stores a user"
metadata:
  type: project
  modified: 2026-11-18
---

# User id columns have no foreign key to auth.users

Decided in PLAT-005 (#15), 2026-11-18. Columns that hold a Supabase user id are plain `uuid NOT NULL`, with **no** `REFERENCES auth.users`. Today those are `created_by`, `user_id`, `invited_by` and `actor_id` (see `backend/migrations/00001_init.sql`).

**Why:** `auth.users` exists only in a Supabase database. With a foreign key to it, every migration, integration test (`dbtest` uses plain `postgres:17-alpine`) and CI run would need Supabase's auth schema or a fake of it. The API already knows the user is real, because it validates the Supabase JWT (PLAT-006) before writing.

**How to apply:**
- New tables follow the same rule. Foreign keys between our own tables (workspace, project and so on) are still required, usually with `ON DELETE CASCADE`.
- Removing data when a Supabase user is deleted is an application concern. It isn't built yet, so when it is, it should clean up memberships and anonymise `created_by`/`actor_id` instead of relying on the database.
- Never trust a user id that came from a request body. It must come from the authenticated principal.

Related: [[api-envelope-and-error-mapping]]
