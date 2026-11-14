-- Which recipe sites a household searches, where an admin chose
-- differently from the default (on for sites in the household's default
-- language). site is the site's name, as discover.Site names it.

-- +goose Up
CREATE TABLE household_recipe_sites (
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    site         TEXT NOT NULL,
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, site)
) STRICT;

-- +goose Down
DROP TABLE household_recipe_sites;
