-- Meal plan (P3): planned meals. Cook events exist since 00004.

-- +goose Up
CREATE TABLE planned_meals (
    id           TEXT PRIMARY KEY,
    household_id TEXT NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    date         TEXT NOT NULL, -- 'YYYY-MM-DD' in the household's time zone, see store.Date
    recipe_id    TEXT NOT NULL,
    servings     INTEGER NOT NULL CHECK (servings > 0),
    position     INTEGER NOT NULL, -- order within the day; several meals per day are allowed
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    deleted_at   TEXT, -- soft delete for Undo; swept after 24 h
    UNIQUE (household_id, id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, created_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX planned_meals_date ON planned_meals (household_id, date) WHERE deleted_at IS NULL;
CREATE INDEX planned_meals_recipe ON planned_meals (household_id, recipe_id);
CREATE INDEX planned_meals_deleted ON planned_meals (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP TABLE planned_meals;
