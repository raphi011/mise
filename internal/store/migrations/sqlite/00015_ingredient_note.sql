-- Moves what follows the amount of a recipe ingredient ("gemischt" in
-- "600 g gemischt") into its own column, so the amount parses.

-- +goose Up
ALTER TABLE recipe_ingredients ADD COLUMN note TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE recipe_ingredients DROP COLUMN note;
