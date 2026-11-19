-- +goose Up
-- granted_by is a Supabase auth.users id with no foreign key, like the user ids in 00001.

-- Lets agents and grants reference (id, workspace_id) pairs so a row can never
-- point at a runtime, agent or project in another workspace.
ALTER TABLE agent_runtime_instances ADD CONSTRAINT agent_runtime_instances_id_workspace_key UNIQUE (id, workspace_id);
ALTER TABLE projects ADD CONSTRAINT projects_id_workspace_key UNIQUE (id, workspace_id);

-- An agent is identified by (runtime_id, provider, name) from runtime.hello, so
-- re-announcing it keeps its id. Agents a runtime stops announcing go offline
-- rather than being deleted, which keeps their project grants.
CREATE TABLE agents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    runtime_id   uuid NOT NULL,
    provider     text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_]*$' AND length(provider) <= 64),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    version      text CHECK (length(version) <= 64),
    capabilities jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(capabilities) = 'object'),
    status       text NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'offline', 'busy')),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (runtime_id, provider, name),
    UNIQUE (id, workspace_id),
    FOREIGN KEY (runtime_id, workspace_id) REFERENCES agent_runtime_instances (id, workspace_id) ON DELETE CASCADE
);
CREATE INDEX agents_workspace_id_idx ON agents (workspace_id);

CREATE TABLE agent_project_access (
    agent_id     uuid NOT NULL,
    project_id   uuid NOT NULL,
    workspace_id uuid NOT NULL,
    granted_by   uuid NOT NULL,
    granted_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, project_id),
    FOREIGN KEY (agent_id, workspace_id) REFERENCES agents (id, workspace_id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, workspace_id) REFERENCES projects (id, workspace_id) ON DELETE CASCADE
);
CREATE INDEX agent_project_access_project_id_idx ON agent_project_access (project_id);

-- +goose Down
DROP TABLE agent_project_access;
DROP TABLE agents;
ALTER TABLE projects DROP CONSTRAINT projects_id_workspace_key;
ALTER TABLE agent_runtime_instances DROP CONSTRAINT agent_runtime_instances_id_workspace_key;
