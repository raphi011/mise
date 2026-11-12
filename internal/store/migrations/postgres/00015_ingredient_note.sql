-- Must stay structurally identical to migrations/sqlite/00015_ingredient_note.sql;
-- TestSchemaParity compares the two.

-- +goose Up
ALTER TABLE recipe_ingredients ADD COLUMN note text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE recipe_ingredients DROP COLUMN note;
