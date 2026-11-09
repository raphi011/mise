-- Recipes (P2): recipes, their ingredients and steps, tags, favourites and
-- cook events, plus the recipe tag on list entries. See docs/plan.md, Schema.

-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE recipes (
    id               TEXT PRIMARY KEY,
    household_id     TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name             TEXT NOT NULL,
    name_key         TEXT NOT NULL, -- case-folded name for search, filled in Go
    description      TEXT NOT NULL,
    photo            TEXT, -- content hash of the image, see internal/images
    prep_min         INTEGER,
    cook_min         INTEGER,
    servings         INTEGER NOT NULL CHECK (servings > 0),
    source_url       TEXT,
    notes            TEXT NOT NULL,
    notes_by         TEXT,
    notes_at         TEXT,
    draft            INTEGER NOT NULL DEFAULT 0 CHECK (draft IN (0, 1)),
    created_by       TEXT NOT NULL,
    updated_by       TEXT NOT NULL,
    deleted_at       TEXT, -- soft delete for Undo; swept after 24 h
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    UNIQUE (household_id, id),
    FOREIGN KEY (household_id, notes_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, created_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX recipes_household ON recipes (household_id, name_key);
CREATE INDEX recipes_deleted ON recipes (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE recipe_ingredients (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    quantity     REAL, -- structured amount for scaling, when amount_text parses
    unit         TEXT, -- canonical unit key, see groceries.Units
    amount_text  TEXT NOT NULL, -- the amount as typed, may be empty
    optional     INTEGER NOT NULL DEFAULT 0 CHECK (optional IN (0, 1)),
    position     INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX recipe_ingredients_recipe ON recipe_ingredients (household_id, recipe_id, position);
CREATE INDEX recipe_ingredients_item ON recipe_ingredients (household_id, item_id);

CREATE TABLE steps (
    id            TEXT PRIMARY KEY,
    household_id  TEXT NOT NULL,
    recipe_id     TEXT NOT NULL,
    position      INTEGER NOT NULL,
    text          TEXT NOT NULL,
    timer_seconds INTEGER CHECK (timer_seconds > 0),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX steps_recipe ON steps (household_id, recipe_id, position);

CREATE TABLE tags (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         TEXT NOT NULL,
    name_key     TEXT NOT NULL,
    pinned       INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)), -- quick filter on Recipes
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (household_id, id),
    UNIQUE (household_id, name_key)
) STRICT;

CREATE TABLE recipe_tags (
    household_id TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    tag_id       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, recipe_id, tag_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, tag_id) REFERENCES tags (household_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX recipe_tags_tag ON recipe_tags (household_id, tag_id);

CREATE TABLE favourites (
    household_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, user_id, recipe_id),
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX favourites_recipe ON favourites (household_id, recipe_id);

-- Written from P3 on (cook mode, "I made this"); the library's filters and
-- sorts read it already.
CREATE TABLE cook_events (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    recipe_id    TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    cooked_at    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX cook_events_recipe ON cook_events (household_id, recipe_id, cooked_at);

-- list_entries gains recipe_id with a composite foreign key, which needs a
-- table rebuild; see migrations/README.md. Hard-deleting a recipe nulls this
-- column in Go first.
CREATE TABLE list_entries_new (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    list_id      TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    quantity     REAL,
    unit         TEXT,
    amount_text  TEXT NOT NULL,
    note         TEXT,
    added_by     TEXT NOT NULL,
    recipe_id    TEXT, -- the recipe the entry was added from, if any
    checked_at   TEXT,
    deleted_at   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, list_id) REFERENCES shopping_lists (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, added_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE RESTRICT
) STRICT;

INSERT INTO list_entries_new (id, household_id, list_id, item_id, quantity, unit, amount_text, note, added_by,
                              checked_at, deleted_at, created_at, updated_at)
SELECT id, household_id, list_id, item_id, quantity, unit, amount_text, note, added_by,
       checked_at, deleted_at, created_at, updated_at
FROM list_entries;

DROP TABLE list_entries;
ALTER TABLE list_entries_new RENAME TO list_entries;

CREATE INDEX list_entries_list ON list_entries (household_id, list_id);
CREATE INDEX list_entries_item ON list_entries (household_id, item_id);
CREATE INDEX list_entries_recipe ON list_entries (household_id, recipe_id) WHERE recipe_id IS NOT NULL;
CREATE INDEX list_entries_deleted ON list_entries (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TEMP TABLE _fk_check (n INTEGER NOT NULL CHECK (n = 0));
INSERT INTO _fk_check SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE _fk_check;
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN;

CREATE TABLE list_entries_old (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    list_id      TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    quantity     REAL,
    unit         TEXT,
    amount_text  TEXT NOT NULL,
    note         TEXT,
    added_by     TEXT NOT NULL,
    checked_at   TEXT,
    deleted_at   TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (household_id, list_id) REFERENCES shopping_lists (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, added_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

INSERT INTO list_entries_old (id, household_id, list_id, item_id, quantity, unit, amount_text, note, added_by,
                              checked_at, deleted_at, created_at, updated_at)
SELECT id, household_id, list_id, item_id, quantity, unit, amount_text, note, added_by,
       checked_at, deleted_at, created_at, updated_at
FROM list_entries;

DROP TABLE list_entries;
ALTER TABLE list_entries_old RENAME TO list_entries;

CREATE INDEX list_entries_list ON list_entries (household_id, list_id);
CREATE INDEX list_entries_item ON list_entries (household_id, item_id);
CREATE INDEX list_entries_deleted ON list_entries (deleted_at) WHERE deleted_at IS NOT NULL;

DROP TABLE cook_events;
DROP TABLE favourites;
DROP TABLE recipe_tags;
DROP TABLE tags;
DROP TABLE steps;
DROP TABLE recipe_ingredients;
DROP TABLE recipes;

COMMIT;
PRAGMA foreign_keys = ON;
