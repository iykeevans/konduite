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
