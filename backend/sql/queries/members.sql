-- name: ListMembers :many
SELECT user_id, role, email, joined_at FROM workspace_members
WHERE workspace_id = @workspace_id AND (joined_at, user_id) > (@after_joined_at::timestamptz, @after_user_id::uuid)
ORDER BY joined_at, user_id
LIMIT @row_limit;

-- name: LockMembers :many
SELECT user_id, role, email, joined_at FROM workspace_members
WHERE workspace_id = $1
ORDER BY user_id
FOR UPDATE;

-- name: UpdateMemberRole :one
UPDATE workspace_members SET role = @role
WHERE workspace_id = @workspace_id AND user_id = @user_id
RETURNING user_id, role, email, joined_at;

-- name: DeleteMember :exec
DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2;

-- name: MemberEmailExists :one
SELECT EXISTS (SELECT 1 FROM workspace_members WHERE workspace_id = $1 AND email = $2);

-- name: DeleteExpiredInvitation :exec
DELETE FROM invitations
WHERE workspace_id = $1 AND email = $2 AND accepted_at IS NULL AND expires_at <= now();

-- name: InsertInvitation :one
INSERT INTO invitations (workspace_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListOpenInvitations :many
SELECT * FROM invitations
WHERE workspace_id = @workspace_id AND accepted_at IS NULL
  AND (created_at, id) > (@after_created_at::timestamptz, @after_id::uuid)
ORDER BY created_at, id
LIMIT @row_limit;

-- name: DeleteOpenInvitation :one
DELETE FROM invitations
WHERE id = $1 AND workspace_id = $2 AND accepted_at IS NULL
RETURNING *;

-- name: LockInvitationByTokenHash :one
SELECT * FROM invitations WHERE token_hash = $1 FOR UPDATE;

-- name: MarkInvitationAccepted :exec
UPDATE invitations SET accepted_at = now(), accepted_by = @accepted_by WHERE id = @id;
