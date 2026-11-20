-- name: InsertRuntime :one
INSERT INTO agent_runtime_instances (workspace_id, owner_id, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: InsertRuntimeCredential :one
INSERT INTO runtime_credentials (runtime_id, secret_hash)
VALUES ($1, $2)
RETURNING id, runtime_id, created_at;

-- name: GetRuntimeCredential :one
SELECT c.id, c.secret_hash, r.id AS runtime_id, r.workspace_id, r.owner_id
FROM runtime_credentials c
JOIN agent_runtime_instances r ON r.id = c.runtime_id
WHERE c.id = $1;

-- name: TouchRuntimeCredential :exec
UPDATE runtime_credentials SET last_used_at = now() WHERE id = $1;

-- name: DeleteRuntimeCredential :exec
DELETE FROM runtime_credentials WHERE id = $1;

-- name: GetRuntime :one
SELECT r.id, r.workspace_id, r.owner_id, r.name, r.created_at, r.updated_at,
       c.last_used_at AS credential_last_used_at,
       (c.id IS NULL)::boolean AS revoked
FROM agent_runtime_instances r
LEFT JOIN runtime_credentials c ON c.runtime_id = r.id
WHERE r.id = $1;

-- name: ListRuntimes :many
SELECT r.id, r.workspace_id, r.owner_id, r.name, r.created_at, r.updated_at,
       c.last_used_at AS credential_last_used_at,
       (c.id IS NULL)::boolean AS revoked
FROM agent_runtime_instances r
LEFT JOIN runtime_credentials c ON c.runtime_id = r.id
WHERE r.workspace_id = @workspace_id
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (r.created_at, r.id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY r.created_at DESC, r.id DESC
LIMIT @row_limit;
