-- name: ClaimOutboxBatch :many
SELECT id, event_type, subject, payload FROM outbox_events
WHERE published_at IS NULL
ORDER BY created_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkOutboxPublished :exec
UPDATE outbox_events SET published_at = now()
WHERE id = ANY(@ids::uuid[]);
