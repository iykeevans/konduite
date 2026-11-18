-- +goose Up
-- User ids (created_by, user_id, invited_by, actor_id) are Supabase auth.users ids.
-- They have no foreign key so migrations run on plain Postgres (tests, CI);
-- the API validates users from the JWT.

CREATE TABLE workspaces (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(trim(name)) > 0),
    slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL,
    role         text NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'project_manager')),
    joined_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, user_id)
);
CREATE INDEX workspace_members_user_id_idx ON workspace_members (user_id);

CREATE TABLE invitations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email        text NOT NULL CHECK (email = lower(email)),
    role         text NOT NULL CHECK (role IN ('admin', 'developer', 'project_manager')),
    token_hash   text NOT NULL UNIQUE,
    invited_by   uuid NOT NULL,
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
-- At most one open invitation per email per workspace.
CREATE UNIQUE INDEX invitations_open_email_idx ON invitations (workspace_id, email) WHERE accepted_at IS NULL;

CREATE TABLE projects (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (length(trim(name)) > 0),
    description    text NOT NULL DEFAULT '',
    repository_url text,
    created_by     uuid NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

CREATE TABLE project_members (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL,
    role       text NOT NULL CHECK (role IN ('maintainer', 'contributor', 'viewer')),
    joined_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id)
);
CREATE INDEX project_members_user_id_idx ON project_members (user_id);

-- Append-only. workspace_id is NULL for events outside a workspace (sign-in).
CREATE TABLE audit_logs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid REFERENCES workspaces (id) ON DELETE SET NULL,
    actor_type    text NOT NULL CHECK (actor_type IN ('user', 'agent', 'runtime', 'integration', 'system')),
    actor_id      uuid,
    action        text NOT NULL,
    resource_type text NOT NULL,
    resource_id   uuid,
    metadata      jsonb NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_workspace_created_idx ON audit_logs (workspace_id, created_at DESC);

-- Transactional outbox: written in the same transaction as the change it announces.
CREATE TABLE outbox_events (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type   text NOT NULL,
    aggregate_id uuid NOT NULL,
    workspace_id uuid,
    payload      jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_events_unpublished_idx ON outbox_events (created_at) WHERE published_at IS NULL;

-- Webhook dedupe: a provider's event is processed at most once.
CREATE TABLE inbound_events (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider          text NOT NULL,
    external_event_id text NOT NULL,
    payload           jsonb NOT NULL,
    processed_at      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, external_event_id)
);

-- +goose Down
DROP TABLE inbound_events;
DROP TABLE outbox_events;
DROP TABLE audit_logs;
DROP TABLE project_members;
DROP TABLE projects;
DROP TABLE invitations;
DROP TABLE workspace_members;
DROP TABLE workspaces;
