-- name: GetMessageForRouting :one
SELECT m.id, m.conversation_id, m.sequence_number, m.sender_type, m.sender_id,
       c.workspace_id, c.project_id
FROM messages m
JOIN conversations c ON c.id = m.conversation_id
WHERE m.id = $1;

-- name: ListRoutingParticipants :many
SELECT p.participant_type, p.participant_id,
       a.runtime_id AS agent_runtime_id, r.id AS known_runtime_id
FROM conversation_participants p
LEFT JOIN agents a
    ON p.participant_type = 'agent' AND a.id = p.participant_id AND a.workspace_id = @workspace_id
LEFT JOIN agent_runtime_instances r
    ON p.participant_type = 'runtime' AND r.id = p.participant_id AND r.workspace_id = @workspace_id
WHERE p.conversation_id = @conversation_id
ORDER BY p.joined_at, p.id;

-- name: InsertDelivery :one
INSERT INTO message_deliveries (
    workspace_id, message_id, conversation_id, sequence_number,
    recipient_type, recipient_id, runtime_id
) VALUES (
    @workspace_id, @message_id, @conversation_id, @sequence_number,
    @recipient_type, @recipient_id, @runtime_id
)
ON CONFLICT (message_id, recipient_id) DO NOTHING
RETURNING *;

-- name: ListMessageDeliveries :many
SELECT * FROM message_deliveries
WHERE message_id = $1
ORDER BY created_at, id;
