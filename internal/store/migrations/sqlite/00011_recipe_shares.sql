-- Public share links: a recipe's read-only page at /s/{token}, viewable
-- without signing in. At most one link per recipe; revoking deletes the row,
-- and every request looks the token up again, so revocation is immediate.
-- The token is stored as issued (not hashed) so members can copy the link
-- again later; it grants nothing a reader of this database cannot already
-- read. See docs/plan.md, Public recipe links.

-- +goose Up
CREATE TABLE recipe_shares (
    household_id TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    token        TEXT NOT NULL UNIQUE, -- 32 random bytes, base64url; looked up across households
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, recipe_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, created_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

-- +goose Down
DROP TABLE recipe_shares;
