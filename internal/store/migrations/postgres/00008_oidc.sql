-- Must stay structurally identical to migrations/sqlite/00008_oidc.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE user_identities (
    issuer     text NOT NULL,
    subject    text NOT NULL,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (issuer, subject),
    UNIQUE (user_id, issuer)
);

-- +goose Down
DROP TABLE user_identities;
