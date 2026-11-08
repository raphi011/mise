-- Must stay structurally identical to migrations/sqlite/00001_init.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE households (
    id               uuid PRIMARY KEY,
    name             text NOT NULL,
    timezone         text NOT NULL,
    week_start       text NOT NULL CHECK (week_start IN ('mon', 'sun')),
    default_language text NOT NULL,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);

CREATE TABLE users (
    id            uuid PRIMARY KEY,
    username      text NOT NULL,
    username_key  text NOT NULL UNIQUE,
    name          text NOT NULL,
    email         text,
    password_hash text NOT NULL,
    avatar_color  text NOT NULL,
    theme         text NOT NULL CHECK (theme IN ('system', 'light', 'dark')),
    language      text,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

CREATE TABLE memberships (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    role         text NOT NULL CHECK (role IN ('admin', 'member')),
    removed_at   timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, user_id)
);

CREATE INDEX memberships_user ON memberships (user_id);

CREATE TABLE sessions (
    id           text PRIMARY KEY, -- SHA-256 of the session token, hex
    household_id uuid NOT NULL,
    user_id      uuid NOT NULL,
    user_agent   text NOT NULL,
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE CASCADE
);

CREATE INDEX sessions_member ON sessions (household_id, user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE memberships;
DROP TABLE users;
DROP TABLE households;
