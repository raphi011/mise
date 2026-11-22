-- Must stay structurally identical to migrations/sqlite/00019_oauth.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE oauth_clients (
    id            uuid PRIMARY KEY,
    name          text NOT NULL,
    redirect_uris text NOT NULL,
    last_used_at  timestamptz,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

CREATE TABLE oauth_grants (
    id                    uuid PRIMARY KEY,
    household_id          uuid NOT NULL,
    user_id               uuid NOT NULL,
    client_id             uuid NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    scope                 text NOT NULL CHECK (scope IN ('read', 'write')),
    code_hash             text NOT NULL UNIQUE,
    code_challenge        text NOT NULL,
    redirect_uri          text NOT NULL,
    code_expires_at       timestamptz NOT NULL,
    code_used_at          timestamptz,
    access_hash           text UNIQUE,
    access_expires_at     timestamptz,
    refresh_hash          text UNIQUE,
    previous_refresh_hash text UNIQUE,
    refresh_expires_at    timestamptz,
    last_used_at          timestamptz,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE CASCADE
);

CREATE INDEX oauth_grants_member ON oauth_grants (household_id, user_id);
CREATE INDEX oauth_grants_client ON oauth_grants (client_id);

-- +goose Down
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
