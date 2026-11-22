-- name: GetProjectMembership :one
SELECT
    p.workspace_id,
    wm.role AS workspace_role,
    COALESCE(pm.role, '')::text AS project_role
FROM projects p
JOIN workspace_members wm
    ON wm.workspace_id = p.workspace_id AND wm.user_id = sqlc.arg(user_id)
LEFT JOIN project_members pm
    ON pm.project_id = p.id AND pm.user_id = sqlc.arg(user_id)
WHERE p.id = sqlc.arg(project_id);

-- name: GetProjectWorkspaceMember :one
SELECT p.workspace_id, wm.role AS workspace_role, wm.email
FROM projects p
JOIN workspace_members wm
    ON wm.workspace_id = p.workspace_id AND wm.user_id = sqlc.arg(user_id)
WHERE p.id = sqlc.arg(project_id);

-- Rows of users who have since left the workspace are hidden: they no
-- longer grant access (authz requires workspace membership).
-- name: ListProjectMembers :many
SELECT pm.user_id, pm.role, pm.joined_at, wm.email, wm.role AS workspace_role
FROM project_members pm
JOIN projects p ON p.id = pm.project_id
JOIN workspace_members wm
    ON wm.workspace_id = p.workspace_id AND wm.user_id = pm.user_id
WHERE pm.project_id = @project_id
  AND (pm.joined_at, pm.user_id) > (@after_joined_at::timestamptz, @after_user_id::uuid)
ORDER BY pm.joined_at, pm.user_id
LIMIT @row_limit;

-- name: LockProjectMembers :many
SELECT pm.user_id, pm.role, pm.joined_at, wm.email, wm.role AS workspace_role
FROM project_members pm
JOIN projects p ON p.id = pm.project_id
LEFT JOIN workspace_members wm
    ON wm.workspace_id = p.workspace_id AND wm.user_id = pm.user_id
WHERE pm.project_id = $1
ORDER BY pm.user_id
FOR UPDATE OF pm;

-- name: InsertProjectMember :one
INSERT INTO project_members (project_id, user_id, role)
VALUES ($1, $2, $3)
RETURNING user_id, role, joined_at;

-- name: UpdateProjectMemberRole :one
UPDATE project_members SET role = @role
WHERE project_id = @project_id AND user_id = @user_id
RETURNING user_id, role, joined_at;

-- name: DeleteProjectMember :exec
DELETE FROM project_members WHERE project_id = $1 AND user_id = $2;
