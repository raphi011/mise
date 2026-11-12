-- Groceries (P1b): categories, the item catalogue, shopping lists and their
-- entries.

-- +goose Up
CREATE TABLE categories (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         TEXT NOT NULL,
    name_key     TEXT NOT NULL, -- case-folded name, filled in Go
    color        TEXT NOT NULL, -- palette name, see groceries.Colors
    position     INTEGER NOT NULL, -- aisle order; uncategorised sorts last
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (household_id, id),
    UNIQUE (household_id, name_key)
) STRICT;

CREATE TABLE items (
    id                TEXT PRIMARY KEY,
    household_id      TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name              TEXT NOT NULL,
    name_key          TEXT NOT NULL,
    -- NULL is "Uncategorised". Deleting a category nulls this column in Go
    -- first.
    category_id       TEXT,
    purchase_count    INTEGER NOT NULL DEFAULT 0,
    last_purchased_at TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    UNIQUE (household_id, id),
    UNIQUE (household_id, name_key),
    FOREIGN KEY (household_id, category_id) REFERENCES categories (household_id, id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX items_category ON items (household_id, category_id);

CREATE TABLE shopping_lists (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         TEXT NOT NULL,
    position     INTEGER NOT NULL, -- the first list is the default
    deleted_at   TEXT, -- soft delete for Undo; swept after 24 h
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (household_id, id)
) STRICT;

CREATE INDEX shopping_lists_household ON shopping_lists (household_id, position);

CREATE TABLE list_entries (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    list_id      TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    quantity     REAL, -- structured amount, when amount_text parses
    unit         TEXT, -- canonical unit key, see groceries.Units
    amount_text  TEXT NOT NULL, -- the amount as typed, may be empty
    note         TEXT,
    added_by     TEXT NOT NULL,
    checked_at   TEXT, -- in the cart
    deleted_at   TEXT, -- removed or cleared; swept after 24 h
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, list_id) REFERENCES shopping_lists (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, added_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX list_entries_list ON list_entries (household_id, list_id);
CREATE INDEX list_entries_item ON list_entries (household_id, item_id);
-- The soft-delete sweep runs across households.
CREATE INDEX list_entries_deleted ON list_entries (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX shopping_lists_deleted ON shopping_lists (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP TABLE list_entries;
DROP TABLE shopping_lists;
DROP TABLE items;
DROP TABLE categories;
