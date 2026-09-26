-- +goose Up
-- The shape of the saved state. A server skips snapshots in a format it
-- doesn't write and replays the events instead.
ALTER TABLE snapshots ADD COLUMN format SMALLINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE snapshots DROP COLUMN format;
