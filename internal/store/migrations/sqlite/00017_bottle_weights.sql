-- Lets an item have a weight per bottle, so "1 Flasche" can be weighed. The
-- unit CHECK changes, which SQLite can only do by rebuilding the table (see
-- README.md). item_unit_weights has no referencing tables.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE item_unit_weights_new (
    household_id TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    unit         TEXT NOT NULL CHECK (unit IN ('piece', 'clove', 'slice', 'bunch', 'can', 'bottle', 'pack')),
    grams        REAL NOT NULL CHECK (grams > 0),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, item_id, unit),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE
) STRICT;
INSERT INTO item_unit_weights_new (household_id, item_id, unit, grams, created_at, updated_at)
    SELECT household_id, item_id, unit, grams, created_at, updated_at FROM item_unit_weights;
DROP TABLE item_unit_weights;
ALTER TABLE item_unit_weights_new RENAME TO item_unit_weights;
CREATE TEMP TABLE _fk_check (n INTEGER NOT NULL CHECK (n = 0));
INSERT INTO _fk_check SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE _fk_check;
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE item_unit_weights_old (
    household_id TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    unit         TEXT NOT NULL CHECK (unit IN ('piece', 'clove', 'slice', 'bunch', 'can', 'pack')),
    grams        REAL NOT NULL CHECK (grams > 0),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, item_id, unit),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE
) STRICT;
INSERT INTO item_unit_weights_old (household_id, item_id, unit, grams, created_at, updated_at)
    SELECT household_id, item_id, unit, grams, created_at, updated_at FROM item_unit_weights WHERE unit <> 'bottle';
DROP TABLE item_unit_weights;
ALTER TABLE item_unit_weights_old RENAME TO item_unit_weights;
COMMIT;
PRAGMA foreign_keys = ON;
