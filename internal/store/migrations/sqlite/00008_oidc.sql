-- Single sign-on links: an OpenID Connect identity (issuer, subject) signs in
-- as one account. Global like sessions. See docs/plan.md, Auth.

-- +goose Up
CREATE TABLE user_identities (
    issuer     TEXT NOT NULL,
    subject    TEXT NOT NULL,
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (issuer, subject),
    UNIQUE (user_id, issuer)
) STRICT;

-- +goose Down
DROP TABLE user_identities;
