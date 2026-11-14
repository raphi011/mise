-- Must stay structurally identical to migrations/sqlite/00018_recipe_sites.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE household_recipe_sites (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    site         text NOT NULL,
    enabled      boolean NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, site)
);

-- +goose Down
DROP TABLE household_recipe_sites;
