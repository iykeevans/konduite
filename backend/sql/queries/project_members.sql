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
