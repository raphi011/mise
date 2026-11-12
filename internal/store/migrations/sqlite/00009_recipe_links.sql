-- Linked recipes ("Goes well with"): a link joins two recipes of a household
-- both ways, stored as one row per direction so each side reads its links
-- by recipe_id. The recipes service writes and deletes both rows together.
--

-- +goose Up
CREATE TABLE recipe_links (
    household_id TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    linked_id    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, recipe_id, linked_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, linked_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    CHECK (recipe_id <> linked_id)
) STRICT;

CREATE INDEX recipe_links_linked ON recipe_links (household_id, linked_id);

-- +goose Down
DROP TABLE recipe_links;
