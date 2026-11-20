-- +goose Up
-- The email a member joined with, so an invitation to someone already in the
-- workspace can be refused. Null for rows that predate it. It is not kept in
-- sync if the user later changes their Supabase email.
ALTER TABLE workspace_members ADD COLUMN email text CHECK (email = lower(email));
CREATE UNIQUE INDEX workspace_members_email_idx ON workspace_members (workspace_id, email) WHERE email IS NOT NULL;

ALTER TABLE invitations ADD COLUMN accepted_by uuid;

-- +goose Down
ALTER TABLE invitations DROP COLUMN accepted_by;
DROP INDEX workspace_members_email_idx;
ALTER TABLE workspace_members DROP COLUMN email;
