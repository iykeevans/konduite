-- name: InsertProject :one
INSERT INTO projects (workspace_id, name, description, repository_url, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: InsertProjectCreatorMembership :exec
INSERT INTO project_members (project_id, user_id, role)
VALUES ($1, $2, 'maintainer');

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1;

-- Members whose workspace role is in see_all_roles see every project;
-- everyone else only the projects they have a project_members row in.
-- name: ListVisibleProjects :many
SELECT p.*
FROM projects p
JOIN workspace_members wm
    ON wm.workspace_id = p.workspace_id AND wm.user_id = @user_id
WHERE p.workspace_id = @workspace_id
  AND p.name > @after_name::text
  AND (
    wm.role = ANY(@see_all_roles::text[])
    OR EXISTS (
        SELECT 1 FROM project_members pm
        WHERE pm.project_id = p.id AND pm.user_id = @user_id
    )
  )
ORDER BY p.name
LIMIT @row_limit;

-- name: LockProject :one
SELECT * FROM projects WHERE id = $1 FOR UPDATE;

-- name: UpdateProject :one
UPDATE projects
SET name = $2, description = $3, repository_url = $4, updated_at = now()
WHERE id = $1
RETURNING *;
