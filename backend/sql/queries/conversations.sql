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

-- Project conversations are visible to the viewer only where authz allows
-- project.view: workspace owner/admin, or any project_members row. Keep this in
-- step with authz.projectAllowed and implicitMaintainers.
-- name: ListConversations :many
SELECT c.* FROM conversations c
WHERE c.workspace_id = @workspace_id
  AND (sqlc.narg(project_id)::uuid IS NULL OR c.project_id = sqlc.narg(project_id)::uuid)
  AND (c.project_id IS NULL
       OR EXISTS (SELECT 1 FROM workspace_members wm
                  WHERE wm.workspace_id = c.workspace_id AND wm.user_id = @viewer_id
                    AND wm.role IN ('owner', 'admin'))
       OR EXISTS (SELECT 1 FROM project_members pm
                  WHERE pm.project_id = c.project_id AND pm.user_id = @viewer_id))
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (c.created_at, c.id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY c.created_at DESC, c.id DESC
LIMIT @row_limit;
