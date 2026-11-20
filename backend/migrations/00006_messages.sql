-- +goose Up
-- sender_id has no foreign key: it points at a user, agent or runtime
-- depending on sender_type, like conversation_participants.participant_id.
--
-- sequence_number comes from conversations.last_seq, bumped in the same
-- transaction as the insert (see internal/messages). It is never filled in by
-- the database so that a rolled-back write cannot leave a gap.
--
-- idempotency_key is NOT NULL so the unique key always applies: NULLs never
-- conflict, which would let a retry create a second message.
CREATE TABLE messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    sequence_number bigint NOT NULL CHECK (sequence_number > 0),
    sender_type     text NOT NULL CHECK (sender_type IN ('user', 'agent', 'runtime')),
    sender_id       uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    parent_id       uuid,
    message_type    text NOT NULL DEFAULT 'text' CHECK (message_type IN ('text', 'task', 'system')),
    content         text NOT NULL CHECK (length(content) <= 65536),
    metadata        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_conversation_seq_key UNIQUE (conversation_id, sequence_number),
    CONSTRAINT messages_idempotency_key UNIQUE (conversation_id, sender_id, idempotency_key),
    CONSTRAINT messages_id_conversation_key UNIQUE (id, conversation_id),
    -- A reply must be in its parent's conversation.
    CONSTRAINT messages_parent_fkey FOREIGN KEY (parent_id, conversation_id) REFERENCES messages (id, conversation_id)
);
CREATE INDEX messages_parent_id_idx ON messages (parent_id) WHERE parent_id IS NOT NULL;

-- +goose Down
DROP TABLE messages;
