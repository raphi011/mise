-- Must stay structurally identical to migrations/sqlite/00009_recipe_links.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE recipe_links (
    household_id uuid NOT NULL,
    recipe_id    uuid NOT NULL,
    linked_id    uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, recipe_id, linked_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, linked_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    CHECK (recipe_id <> linked_id)
);

CREATE INDEX recipe_links_linked ON recipe_links (household_id, linked_id);

-- +goose Down
DROP TABLE recipe_links;
