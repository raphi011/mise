-- Must stay structurally identical to migrations/sqlite/00012_api_tokens.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL,
    user_id      uuid NOT NULL,
    name         text NOT NULL,
    token_hash   text NOT NULL UNIQUE,
    scope        text NOT NULL CHECK (scope IN ('read', 'write')),
    expires_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE CASCADE
);

CREATE INDEX api_tokens_member ON api_tokens (household_id, user_id);

-- +goose Down
DROP TABLE api_tokens;
