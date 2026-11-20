-- name: NextConversationSeq :one
UPDATE conversations SET last_seq = last_seq + 1
WHERE id = $1
RETURNING last_seq;

-- name: InsertMessage :one
INSERT INTO messages (
    conversation_id, sequence_number, sender_type, sender_id, idempotency_key,
    parent_id, message_type, content, metadata
) VALUES (
    @conversation_id, @sequence_number, @sender_type, @sender_id, @idempotency_key,
    @parent_id, @message_type, @content, @metadata
)
ON CONFLICT (conversation_id, sender_id, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetMessageByIdempotencyKey :one
SELECT * FROM messages
WHERE conversation_id = $1 AND sender_id = $2 AND idempotency_key = $3;

-- name: GetMessage :one
SELECT * FROM messages WHERE id = $1;
