-- Readable recipe addresses: /recipes/{slug} instead of the recipe ID. The
-- slug is made from the name in Go and is unique within the household.
-- recipe_slugs keeps the slugs a recipe had before a rename, so old links
-- redirect to the current one. Existing recipes start with their ID as the
-- slug; jobs.SlugRecipes replaces it on the next start.

-- +goose Up
ALTER TABLE recipes ADD COLUMN slug TEXT NOT NULL DEFAULT '';
UPDATE recipes SET slug = id;
CREATE UNIQUE INDEX recipes_slug ON recipes (household_id, slug);

CREATE TABLE recipe_slugs (
    household_id TEXT NOT NULL,
    slug         TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, slug),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX recipe_slugs_recipe ON recipe_slugs (household_id, recipe_id);

-- +goose Down
DROP TABLE recipe_slugs;
DROP INDEX recipes_slug;
ALTER TABLE recipes DROP COLUMN slug;
