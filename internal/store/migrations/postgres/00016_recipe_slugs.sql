-- Must stay structurally identical to migrations/sqlite/00016_recipe_slugs.sql;
-- TestSchemaParity compares the two.

-- +goose Up
ALTER TABLE recipes ADD COLUMN slug text NOT NULL DEFAULT '';
UPDATE recipes SET slug = id::text;
CREATE UNIQUE INDEX recipes_slug ON recipes (household_id, slug);

CREATE TABLE recipe_slugs (
    household_id uuid NOT NULL,
    slug         text NOT NULL,
    recipe_id    uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, slug),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
);

CREATE INDEX recipe_slugs_recipe ON recipe_slugs (household_id, recipe_id);

-- +goose Down
DROP TABLE recipe_slugs;
DROP INDEX recipes_slug;
ALTER TABLE recipes DROP COLUMN slug;
