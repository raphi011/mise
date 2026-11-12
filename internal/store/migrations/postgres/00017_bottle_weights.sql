-- Must stay structurally identical to migrations/sqlite/00017_bottle_weights.sql;
-- TestSchemaParity compares the two.

-- +goose Up
ALTER TABLE item_unit_weights DROP CONSTRAINT item_unit_weights_unit_check;
ALTER TABLE item_unit_weights ADD CONSTRAINT item_unit_weights_unit_check
    CHECK (unit IN ('piece', 'clove', 'slice', 'bunch', 'can', 'bottle', 'pack'));

-- +goose Down
DELETE FROM item_unit_weights WHERE unit = 'bottle';
ALTER TABLE item_unit_weights DROP CONSTRAINT item_unit_weights_unit_check;
ALTER TABLE item_unit_weights ADD CONSTRAINT item_unit_weights_unit_check
    CHECK (unit IN ('piece', 'clove', 'slice', 'bunch', 'can', 'pack'));
