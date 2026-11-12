-- Invites and server-wide settings (P1a).

-- +goose Up
CREATE TABLE invites (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE, -- SHA-256 of the invite token, hex
    email        TEXT,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    invited_by   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    used_at      TEXT,
    used_by      TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, invited_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, used_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX invites_household ON invites (household_id, expires_at);

-- Server-wide settings that belong to no household, e.g. base_url when
-- MISE_BASE_URL is unset. One row per key.
CREATE TABLE server_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE server_settings;
DROP TABLE invites;
