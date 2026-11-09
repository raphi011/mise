-- Must stay structurally identical to migrations/sqlite/00005_plan.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE planned_meals (
    id           uuid PRIMARY KEY,
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    date         date NOT NULL,
    recipe_id    uuid NOT NULL,
    servings     integer NOT NULL CHECK (servings > 0),
    position     integer NOT NULL,
    created_by   uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    deleted_at   timestamptz,
    UNIQUE (household_id, id),
    FOREIGN KEY (household_id, recipe_id) REFERENCES recipes (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, created_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE INDEX planned_meals_date ON planned_meals (household_id, date) WHERE deleted_at IS NULL;
CREATE INDEX planned_meals_recipe ON planned_meals (household_id, recipe_id);
CREATE INDEX planned_meals_deleted ON planned_meals (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP TABLE planned_meals;
