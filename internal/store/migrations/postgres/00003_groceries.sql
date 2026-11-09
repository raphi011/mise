-- Must stay structurally identical to migrations/sqlite/00003_groceries.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE categories (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         text NOT NULL,
    name_key     text NOT NULL,
    color        text NOT NULL,
    position     integer NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    UNIQUE (household_id, id),
    UNIQUE (household_id, name_key)
);

CREATE TABLE items (
    id                uuid PRIMARY KEY,
    household_id      uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name              text NOT NULL,
    name_key          text NOT NULL,
    category_id       uuid,
    purchase_count    integer NOT NULL DEFAULT 0,
    last_purchased_at timestamptz,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    UNIQUE (household_id, id),
    UNIQUE (household_id, name_key),
    FOREIGN KEY (household_id, category_id) REFERENCES categories (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX items_category ON items (household_id, category_id);

CREATE TABLE shopping_lists (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         text NOT NULL,
    position     integer NOT NULL,
    deleted_at   timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    UNIQUE (household_id, id)
);

CREATE INDEX shopping_lists_household ON shopping_lists (household_id, position);

CREATE TABLE list_entries (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL,
    list_id      uuid NOT NULL,
    item_id      uuid NOT NULL,
    quantity     double precision,
    unit         text,
    amount_text  text NOT NULL,
    note         text,
    added_by     uuid NOT NULL,
    checked_at   timestamptz,
    deleted_at   timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, list_id) REFERENCES shopping_lists (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, added_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE INDEX list_entries_list ON list_entries (household_id, list_id);
CREATE INDEX list_entries_item ON list_entries (household_id, item_id);
CREATE INDEX list_entries_deleted ON list_entries (deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX shopping_lists_deleted ON shopping_lists (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP TABLE list_entries;
DROP TABLE shopping_lists;
DROP TABLE items;
DROP TABLE categories;
