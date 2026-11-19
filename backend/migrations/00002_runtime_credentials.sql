-- +goose Up
-- owner_id is a Supabase auth.users id with no foreign key, like the user ids in 00001.

CREATE TABLE agent_runtime_instances (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    owner_id     uuid NOT NULL,
    name         text NOT NULL CHECK (length(trim(name)) > 0 AND length(name) <= 100),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_runtime_instances_workspace_id_idx ON agent_runtime_instances (workspace_id);
CREATE INDEX agent_runtime_instances_owner_id_idx ON agent_runtime_instances (owner_id);

-- Only the SHA-256 of the refresh secret is stored; the secret itself is
-- returned once at registration. Revoking a runtime deletes its row.
CREATE TABLE runtime_credentials (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    runtime_id   uuid NOT NULL UNIQUE REFERENCES agent_runtime_instances (id) ON DELETE CASCADE,
    secret_hash  bytea NOT NULL CHECK (length(secret_hash) = 32),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

-- +goose Down
DROP TABLE runtime_credentials;
DROP TABLE agent_runtime_instances;
