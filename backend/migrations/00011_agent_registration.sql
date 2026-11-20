-- +goose Up
-- Labels a user gives an agent. runtime.hello never writes them, so they
-- survive reconnects; the runtime stays the authority on version and
-- capabilities.
ALTER TABLE agents
    ADD COLUMN display_name text CHECK (length(display_name) BETWEEN 1 AND 128),
    ADD COLUMN description  text CHECK (length(description) BETWEEN 1 AND 1000);

-- +goose Down
ALTER TABLE agents DROP COLUMN description, DROP COLUMN display_name;
