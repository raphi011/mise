-- Must stay structurally identical to migrations/sqlite/00011_recipe_shares.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE recipe_shares (
    household_id uuid NOT NULL,
    recipe_id    uuid NOT NULL,
    token        text NOT NULL UNIQUE,
    created_by   uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, recipe_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, created_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

-- +goose Down
DROP TABLE recipe_shares;
