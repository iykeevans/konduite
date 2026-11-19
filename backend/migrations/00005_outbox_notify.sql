-- +goose Up
-- Wakes the outbox relay (LISTEN outbox_events). Postgres delivers the
-- notification only when the inserting transaction commits, so the relay
-- never wakes for a row it can't see yet. The unpublished-rows index is in 00001.

-- +goose StatementBegin
CREATE FUNCTION notify_outbox_events() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('outbox_events', '');
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER outbox_events_notify
AFTER INSERT ON outbox_events
FOR EACH STATEMENT EXECUTE FUNCTION notify_outbox_events();

-- +goose Down
DROP TRIGGER outbox_events_notify ON outbox_events;
DROP FUNCTION notify_outbox_events();
