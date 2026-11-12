-- Measurement system a user reads amounts and temperatures in. Recipes are
-- stored as written and converted when shown.

-- +goose Up
ALTER TABLE users ADD COLUMN units TEXT NOT NULL DEFAULT 'metric' CHECK (units IN ('metric', 'us'));

-- +goose Down
ALTER TABLE users DROP COLUMN units;
