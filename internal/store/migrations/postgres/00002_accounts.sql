-- Must stay structurally identical to migrations/sqlite/00002_accounts.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE invites (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL,
    token_hash   text NOT NULL UNIQUE, -- SHA-256 of the invite token, hex
    email        text,
    role         text NOT NULL CHECK (role IN ('admin', 'member')),
    invited_by   uuid NOT NULL,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    used_by      uuid,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, invited_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, used_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE INDEX invites_household ON invites (household_id, expires_at);

CREATE TABLE server_settings (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE server_settings;
DROP TABLE invites;
