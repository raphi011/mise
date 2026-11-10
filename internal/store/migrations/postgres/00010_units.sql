-- Must stay structurally identical to migrations/sqlite/00010_units.sql;
-- TestSchemaParity compares the two.

-- +goose Up
ALTER TABLE users ADD COLUMN units text NOT NULL DEFAULT 'metric' CHECK (units IN ('metric', 'us'));

-- +goose Down
ALTER TABLE users DROP COLUMN units;
