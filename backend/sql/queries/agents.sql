-- name: LockRuntimeForAgentSync :one
SELECT workspace_id FROM agent_runtime_instances WHERE id = $1 FOR NO KEY UPDATE;

-- name: UpsertAnnouncedAgent :one
INSERT INTO agents (workspace_id, runtime_id, provider, name, version, capabilities, status, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, 'online', now())
ON CONFLICT (runtime_id, provider, name) DO UPDATE
SET version = EXCLUDED.version,
    capabilities = EXCLUDED.capabilities,
    status = 'online',
    last_seen_at = EXCLUDED.last_seen_at,
    updated_at = now()
RETURNING *;

-- name: MarkUnannouncedAgentsOffline :exec
UPDATE agents SET status = 'offline', updated_at = now()
WHERE runtime_id = @runtime_id AND status <> 'offline' AND NOT (id = ANY(@announced_ids::uuid[]));

-- name: ListRuntimeAgents :many
SELECT * FROM agents WHERE runtime_id = $1 ORDER BY provider, name;

-- name: AgentHasProjectAccess :one
SELECT EXISTS (SELECT 1 FROM agent_project_access WHERE agent_id = $1 AND project_id = $2);

-- name: GetAgentRuntime :one
SELECT id, workspace_id, owner_id FROM agent_runtime_instances WHERE id = $1;

-- name: InsertRegisteredAgent :one
INSERT INTO agents (workspace_id, runtime_id, provider, name, display_name, description, capabilities, status, last_seen_at)
VALUES (@workspace_id, @runtime_id, @provider, @name, @display_name, @description, @capabilities, 'offline', now())
RETURNING *;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: LockAgent :one
SELECT * FROM agents WHERE id = $1 FOR NO KEY UPDATE;

-- name: ListAgents :many
SELECT * FROM agents
WHERE workspace_id = @workspace_id
  AND (sqlc.narg(runtime_id)::uuid IS NULL OR runtime_id = sqlc.narg(runtime_id)::uuid)
  AND (sqlc.narg(after_created_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;

-- name: UpdateAgentLabels :one
UPDATE agents SET display_name = @display_name, description = @description, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteAgent :execrows
DELETE FROM agents WHERE id = $1;
