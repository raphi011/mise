-- Must stay structurally identical to migrations/sqlite/00004_recipes.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE recipes (
    id               uuid PRIMARY KEY,
    household_id     uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name             text NOT NULL,
    name_key         text NOT NULL,
    description      text NOT NULL,
    photo            text,
    prep_min         integer,
    cook_min         integer,
    servings         integer NOT NULL CHECK (servings > 0),
    source_url       text,
    notes            text NOT NULL,
    notes_by         uuid,
    notes_at         timestamptz,
    draft            boolean NOT NULL DEFAULT false,
    created_by       uuid NOT NULL,
    updated_by       uuid NOT NULL,
    deleted_at       timestamptz,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    UNIQUE (household_id, id),
    FOREIGN KEY (household_id, notes_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, created_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE INDEX recipes_household ON recipes (household_id, name_key);
CREATE INDEX recipes_deleted ON recipes (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE recipe_ingredients (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL,
    recipe_id    uuid NOT NULL,
    item_id      uuid NOT NULL,
    quantity     double precision,
    unit         text,
    amount_text  text NOT NULL,
    optional     boolean NOT NULL DEFAULT false,
    position     integer NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE RESTRICT
);

CREATE INDEX recipe_ingredients_recipe ON recipe_ingredients (household_id, recipe_id, position);
CREATE INDEX recipe_ingredients_item ON recipe_ingredients (household_id, item_id);

CREATE TABLE steps (
    id            uuid PRIMARY KEY,
    household_id  uuid NOT NULL,
    recipe_id     uuid NOT NULL,
    position      integer NOT NULL,
    text          text NOT NULL,
    timer_seconds integer CHECK (timer_seconds > 0),
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
);

CREATE INDEX steps_recipe ON steps (household_id, recipe_id, position);

CREATE TABLE tags (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         text NOT NULL,
    name_key     text NOT NULL,
    pinned       boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    UNIQUE (household_id, id),
    UNIQUE (household_id, name_key)
);

CREATE TABLE recipe_tags (
    household_id uuid NOT NULL,
    recipe_id    uuid NOT NULL,
    tag_id       uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, recipe_id, tag_id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, tag_id) REFERENCES tags (household_id, id) ON DELETE CASCADE
);

CREATE INDEX recipe_tags_tag ON recipe_tags (household_id, tag_id);

CREATE TABLE favourites (
    household_id uuid NOT NULL,
    user_id      uuid NOT NULL,
    recipe_id    uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, user_id, recipe_id),
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE
);

CREATE INDEX favourites_recipe ON favourites (household_id, recipe_id);

CREATE TABLE cook_events (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL,
    recipe_id    uuid NOT NULL,
    user_id      uuid NOT NULL,
    cooked_at    timestamptz NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, user_id) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE INDEX cook_events_recipe ON cook_events (household_id, recipe_id, cooked_at);

ALTER TABLE list_entries ADD COLUMN recipe_id uuid;
ALTER TABLE list_entries ADD FOREIGN KEY (household_id, recipe_id)
    REFERENCES recipes (household_id, id) ON DELETE RESTRICT;
CREATE INDEX list_entries_recipe ON list_entries (household_id, recipe_id) WHERE recipe_id IS NOT NULL;

-- +goose Down
DROP INDEX list_entries_recipe;
ALTER TABLE list_entries DROP COLUMN recipe_id;
DROP TABLE cook_events;
DROP TABLE favourites;
DROP TABLE recipe_tags;
DROP TABLE tags;
DROP TABLE steps;
DROP TABLE recipe_ingredients;
DROP TABLE recipes;
