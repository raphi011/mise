-- Conventions (see docs/plan.md, Database layer):
-- * IDs are UUIDv7 as TEXT; timestamps are fixed-width UTC TEXT bound from Go.
-- * Household-scoped tables carry household_id and use composite foreign keys.
-- * Composite foreign keys use ON DELETE RESTRICT or CASCADE, never SET NULL.

-- +goose Up
CREATE TABLE households (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    timezone         TEXT NOT NULL,
    week_start       TEXT NOT NULL CHECK (week_start IN ('mon', 'sun')),
    default_language TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
) STRICT;

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL,
    username_key  TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    email         TEXT,
    password_hash TEXT NOT NULL,
    avatar_color  TEXT NOT NULL,
    theme         TEXT NOT NULL CHECK (theme IN ('system', 'light', 'dark')),
    language      TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
) STRICT;

CREATE TABLE memberships (
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    removed_at   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, user_id)
) STRICT;

CREATE INDEX memberships_user ON memberships (user_id);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY, -- SHA-256 of the session token, hex
    household_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    user_agent   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE CASCADE
) STRICT;

CREATE INDEX sessions_member ON sessions (household_id, user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE memberships;
DROP TABLE users;
DROP TABLE households;
