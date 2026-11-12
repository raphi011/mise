-- Nutrition: values per 100 g that a household typed for an item, and the
-- weights that turn "2 tbsp" or "3 cloves" into grams. Per-serving nutrition
-- of a recipe is derived from these when the page renders and is not
-- stored.

-- +goose Up
CREATE TABLE item_nutrition (
    household_id    TEXT NOT NULL,
    item_id         TEXT NOT NULL,
    kcal            REAL NOT NULL CHECK (kcal >= 0 AND kcal <= 900),
    protein_g       REAL CHECK (protein_g >= 0 AND protein_g <= 100),
    fat_g           REAL CHECK (fat_g >= 0 AND fat_g <= 100),
    saturated_fat_g REAL CHECK (saturated_fat_g >= 0 AND saturated_fat_g <= 100),
    carbs_g         REAL CHECK (carbs_g >= 0 AND carbs_g <= 100),
    sugars_g        REAL CHECK (sugars_g >= 0 AND sugars_g <= 100),
    fibre_g         REAL CHECK (fibre_g >= 0 AND fibre_g <= 100),
    sodium_mg       REAL CHECK (sodium_mg >= 0 AND sodium_mg <= 40000),
    density_g_ml    REAL CHECK (density_g_ml > 0), -- grams per millilitre, for volume amounts
    updated_by      TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (household_id, item_id),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE,
    FOREIGN KEY (household_id, updated_by) REFERENCES memberships (household_id, user_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE item_unit_weights (
    household_id TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    unit         TEXT NOT NULL CHECK (unit IN ('piece', 'clove', 'slice', 'bunch', 'can', 'pack')),
    grams        REAL NOT NULL CHECK (grams > 0),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, item_id, unit),
    FOREIGN KEY (household_id, item_id) REFERENCES items (household_id, id) ON DELETE CASCADE
) STRICT;

-- +goose Down
DROP TABLE item_unit_weights;
DROP TABLE item_nutrition;
