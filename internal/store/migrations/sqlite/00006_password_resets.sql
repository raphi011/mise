-- Self-service password reset by email (P4). Global like sessions: an
-- account's password is not a household's.

-- +goose Up
CREATE TABLE password_resets (
    id         TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE, -- SHA-256 of the reset token, hex
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX password_resets_user ON password_resets (user_id);
CREATE INDEX password_resets_expires ON password_resets (expires_at);

-- +goose Down
DROP TABLE password_resets;
