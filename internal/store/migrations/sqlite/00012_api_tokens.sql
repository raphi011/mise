-- API tokens: bearer credentials for scripts and agents, acting as one member
-- of one household with a read or read/write scope. Only the SHA-256 of the
-- token is stored. See docs/plan.md, API.

-- +goose Up
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE, -- SHA-256 of the token, hex
    scope        TEXT NOT NULL CHECK (scope IN ('read', 'write')),
    expires_at   TEXT,
    last_used_at TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE CASCADE
) STRICT;

CREATE INDEX api_tokens_member ON api_tokens (household_id, user_id);

-- +goose Down
DROP TABLE api_tokens;
