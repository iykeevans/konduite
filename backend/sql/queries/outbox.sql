-- name: InsertOutboxEvent :one
INSERT INTO outbox_events (event_type, aggregate_id, workspace_id, payload)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListUnpublishedOutboxEvents :many
SELECT * FROM outbox_events
WHERE published_at IS NULL
ORDER BY created_at
LIMIT $1;
