-- +goose Up
-- One row per message and recipient, written by the delivery router when it
-- consumes message.created. recipient_id has no foreign key: it points at a
-- user, agent or runtime depending on recipient_type, like
-- conversation_participants.participant_id. A user recipient has no
-- runtime_id. UNIQUE (message_id, recipient_id) is what makes a redelivered
-- message.created a no-op.
--
-- sequence_number is copied from the message so pending deliveries can be
-- pushed in conversation order without a join.
CREATE TABLE message_deliveries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    message_id      uuid NOT NULL,
    conversation_id uuid NOT NULL,
    sequence_number bigint NOT NULL CHECK (sequence_number > 0),
    recipient_type  text NOT NULL CHECK (recipient_type IN ('user', 'agent', 'runtime')),
    recipient_id    uuid NOT NULL,
    runtime_id      uuid,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'acknowledged', 'failed')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,
    acknowledged_at timestamptz,
    CONSTRAINT message_deliveries_message_recipient_key UNIQUE (message_id, recipient_id),
    CONSTRAINT message_deliveries_message_fkey FOREIGN KEY (message_id, conversation_id) REFERENCES messages (id, conversation_id) ON DELETE CASCADE,
    CONSTRAINT message_deliveries_runtime_fkey FOREIGN KEY (runtime_id, workspace_id) REFERENCES agent_runtime_instances (id, workspace_id) ON DELETE CASCADE,
    CONSTRAINT message_deliveries_runtime_recipient_check CHECK (recipient_type = 'user' OR runtime_id IS NOT NULL)
);
CREATE INDEX message_deliveries_runtime_pending_idx ON message_deliveries (runtime_id, conversation_id, sequence_number)
    WHERE status = 'pending' AND runtime_id IS NOT NULL;

-- Lets one outbox row be published on a subject its event type can't name,
-- such as agenthub.rt.<runtime_id>.deliver. NULL means agenthub.<event_type>.
ALTER TABLE outbox_events ADD COLUMN subject text CHECK (subject LIKE 'agenthub.%');

-- +goose Down
ALTER TABLE outbox_events DROP COLUMN subject;
DROP TABLE message_deliveries;
