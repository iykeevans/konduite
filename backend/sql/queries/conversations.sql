-- name: InsertConversation :one
INSERT INTO conversations (workspace_id, project_id, title, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: InsertConversationParticipant :one
INSERT INTO conversation_participants (conversation_id, participant_type, participant_id)
VALUES ($1, $2, $3)
RETURNING *;

-- name: LockProjectInWorkspace :one
SELECT id FROM projects
WHERE id = $1 AND workspace_id = $2
FOR KEY SHARE;

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = $1;

-- name: ListConversationParticipants :many
SELECT * FROM conversation_participants
WHERE conversation_id = $1
ORDER BY joined_at, id;

-- name: ListConversations :many
SELECT * FROM conversations
WHERE workspace_id = @workspace_id
  AND (sqlc.narg(project_id)::uuid IS NULL OR project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;
