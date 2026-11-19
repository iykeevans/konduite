-- +goose Up
-- created_by and participant ids have no foreign key, like the user ids in 00001.
-- A participant id points at a user, agent or runtime depending on
-- participant_type, so it cannot reference a single table.

-- last_seq is the highest message sequence number issued in the conversation.
-- Writers bump it with UPDATE ... RETURNING in the message's transaction; the
-- row lock is what keeps sequence numbers gapless and ordered.
CREATE TABLE conversations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id   uuid REFERENCES projects (id) ON DELETE CASCADE,
    title        text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    created_by   uuid NOT NULL,
    last_seq     bigint NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversations_workspace_created_idx ON conversations (workspace_id, created_at DESC, id DESC);
CREATE INDEX conversations_project_id_idx ON conversations (project_id) WHERE project_id IS NOT NULL;

CREATE TABLE conversation_participants (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id  uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    participant_type text NOT NULL CHECK (participant_type IN ('user', 'agent', 'runtime')),
    participant_id   uuid NOT NULL,
    joined_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, participant_type, participant_id)
);
CREATE INDEX conversation_participants_participant_idx ON conversation_participants (participant_type, participant_id);

-- +goose Down
DROP TABLE conversation_participants;
DROP TABLE conversations;
