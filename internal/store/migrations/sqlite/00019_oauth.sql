-- OAuth 2.1 for MCP clients. Clients register themselves (RFC 7591), so
-- oauth_clients is global like sessions. A grant is one member's consent
-- for one client in one household: it holds the authorization code until
-- it is exchanged, then the current access and refresh tokens. Only
-- SHA-256 hashes of codes and tokens are stored.

-- +goose Up
CREATE TABLE oauth_clients (
    id            TEXT PRIMARY KEY, -- the client_id
    name          TEXT NOT NULL,
    redirect_uris TEXT NOT NULL,    -- one per line
    last_used_at  TEXT,             -- last time a member approved it
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
) STRICT;

CREATE TABLE oauth_grants (
    id                    TEXT PRIMARY KEY,
    household_id          TEXT NOT NULL,
    user_id               TEXT NOT NULL,
    client_id             TEXT NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    scope                 TEXT NOT NULL CHECK (scope IN ('read', 'write')),
    code_hash             TEXT NOT NULL UNIQUE,
    code_challenge        TEXT NOT NULL, -- PKCE S256
    redirect_uri          TEXT NOT NULL,
    code_expires_at       TEXT NOT NULL,
    code_used_at          TEXT,
    access_hash           TEXT UNIQUE,
    access_expires_at     TEXT,
    refresh_hash          TEXT UNIQUE,
    previous_refresh_hash TEXT UNIQUE, -- presenting it again revokes the grant
    refresh_expires_at    TEXT,
    last_used_at          TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE CASCADE
) STRICT;

CREATE INDEX oauth_grants_member ON oauth_grants (household_id, user_id);
CREATE INDEX oauth_grants_client ON oauth_grants (client_id);

-- +goose Down
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
