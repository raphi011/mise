-- Must stay structurally identical to migrations/sqlite/00013_item_catalog.sql;
-- TestSchemaParity compares the two.

-- +goose Up
ALTER TABLE items ADD COLUMN icon text;
ALTER TABLE items ADD COLUMN catalog_key text;
CREATE UNIQUE INDEX items_catalog_key ON items (household_id, catalog_key) WHERE catalog_key IS NOT NULL;
ALTER TABLE categories ADD COLUMN catalog_key text;
CREATE UNIQUE INDEX categories_catalog_key ON categories (household_id, catalog_key) WHERE catalog_key IS NOT NULL;

-- +goose Down
DROP INDEX categories_catalog_key;
ALTER TABLE categories DROP COLUMN catalog_key;
DROP INDEX items_catalog_key;
ALTER TABLE items DROP COLUMN catalog_key;
ALTER TABLE items DROP COLUMN icon;
