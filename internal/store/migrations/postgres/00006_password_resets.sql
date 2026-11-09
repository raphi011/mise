-- Must stay structurally identical to migrations/sqlite/00006_password_resets.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE password_resets (
    id         uuid PRIMARY KEY,
    token_hash text NOT NULL UNIQUE, -- SHA-256 of the reset token, hex
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX password_resets_user ON password_resets (user_id);
CREATE INDEX password_resets_expires ON password_resets (expires_at);

-- +goose Down
DROP TABLE password_resets;
