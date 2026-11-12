-- Must stay structurally identical to migrations/sqlite/00014_nutrition.sql;
-- TestSchemaParity compares the two.

-- +goose Up
CREATE TABLE item_nutrition (
    household_id    uuid NOT NULL,
    item_id         uuid NOT NULL,
    kcal            double precision NOT NULL CHECK (kcal >= 0 AND kcal <= 900),
    protein_g       double precision CHECK (protein_g >= 0 AND protein_g <= 100),
    fat_g           double precision CHECK (fat_g >= 0 AND fat_g <= 100),
    saturated_fat_g double precision CHECK (saturated_fat_g >= 0 AND saturated_fat_g <= 100),
    carbs_g         double precision CHECK (carbs_g >= 0 AND carbs_g <= 100),
    sugars_g        double precision CHECK (sugars_g >= 0 AND sugars_g <= 100),
    fibre_g         double precision CHECK (fibre_g >= 0 AND fibre_g <= 100),
    sodium_mg       double precision CHECK (sodium_mg >= 0 AND sodium_mg <= 40000),
    density_g_ml    double precision CHECK (density_g_ml > 0),
    updated_by      uuid NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    PRIMARY KEY (household_id, item_id),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
);

CREATE TABLE item_unit_weights (
    household_id uuid NOT NULL,
    item_id      uuid NOT NULL,
    unit         text NOT NULL CHECK (unit IN ('piece', 'clove', 'slice', 'bunch', 'can', 'pack')),
    grams        double precision NOT NULL CHECK (grams > 0),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (household_id, item_id, unit),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE item_unit_weights;
DROP TABLE item_nutrition;
