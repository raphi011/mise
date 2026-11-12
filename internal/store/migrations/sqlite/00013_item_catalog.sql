-- Links items and categories to the embedded default-ingredient catalogue
-- and lets an item override its icon. See docs/plan.md, Default ingredients.

-- +goose Up
ALTER TABLE items ADD COLUMN icon TEXT;
ALTER TABLE items ADD COLUMN catalog_key TEXT;
CREATE UNIQUE INDEX items_catalog_key ON items (household_id, catalog_key) WHERE catalog_key IS NOT NULL;
ALTER TABLE categories ADD COLUMN catalog_key TEXT;
CREATE UNIQUE INDEX categories_catalog_key ON categories (household_id, catalog_key) WHERE catalog_key IS NOT NULL;

-- +goose Down
DROP INDEX categories_catalog_key;
ALTER TABLE categories DROP COLUMN catalog_key;
DROP INDEX items_catalog_key;
ALTER TABLE items DROP COLUMN catalog_key;
ALTER TABLE items DROP COLUMN icon;
