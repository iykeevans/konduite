-- name: InsertWorkspace :one
INSERT INTO workspaces (name, slug, created_by)
VALUES ($1, $2, $3)
RETURNING *;

-- name: InsertWorkspaceMember :exec
INSERT INTO workspace_members (workspace_id, user_id, role, email)
VALUES (@workspace_id, @user_id, @role, sqlc.narg('email'));

-- name: GetWorkspaceForMember :one
SELECT w.id, w.name, w.slug, w.created_by, w.created_at, w.updated_at, m.role
FROM workspaces w
JOIN workspace_members m ON m.workspace_id = w.id
WHERE w.id = $1 AND m.user_id = $2;

-- name: ListWorkspacesForMember :many
SELECT w.id, w.name, w.slug, w.created_by, w.created_at, w.updated_at, m.role
FROM workspaces w
JOIN workspace_members m ON m.workspace_id = w.id
WHERE m.user_id = @user_id AND w.slug > @after_slug::text
ORDER BY w.slug
LIMIT @row_limit;

-- name: LockWorkspace :one
SELECT * FROM workspaces WHERE id = $1 FOR UPDATE;

-- name: UpdateWorkspace :one
UPDATE workspaces
SET name = $2, slug = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: InsertAuditLog :exec
INSERT INTO audit_logs (workspace_id, actor_type, actor_id, action, resource_type, resource_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7);
