package store_test

import (
	"context"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestRecipesMigrationKeepsListEntries checks that 00004, which rebuilds
// list_entries on SQLite to add recipe_id, keeps existing entries on the way
// up and down.
func TestRecipesMigrationKeepsListEntries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		if err := db.MigrateDownTo(ctx, 3); err != nil {
			t.Fatalf("down to 3: %v", err)
		}
		h := storetest.NewHousehold(t, db, "mig")
		entry := store.NewID()
		now := store.Now()
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			item, list := store.NewID(), store.NewID()
			if _, err := q.Exec(ctx, `INSERT INTO items (id, household_id, name, name_key, created_at, updated_at)
				VALUES (?, ?, 'Milk', 'milk', ?, ?)`, item, h.ID, now, now); err != nil {
				return err
			}
			if _, err := q.Exec(ctx, `INSERT INTO shopping_lists (id, household_id, name, position, created_at, updated_at)
				VALUES (?, ?, 'Weekly shop', 0, ?, ?)`, list, h.ID, now, now); err != nil {
				return err
			}
			_, err := q.Exec(ctx, `INSERT INTO list_entries (id, household_id, list_id, item_id, amount_text, added_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, '1 l', ?, ?, ?)`, entry, h.ID, list, item, h.Admin, now, now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := db.Migrate(ctx); err != nil {
			t.Fatalf("up: %v", err)
		}
		var amount string
		var recipe *string
		if err := db.System().Read().QueryRow(ctx, `SELECT amount_text, recipe_id FROM list_entries WHERE id = ?`, entry).
			Scan(&amount, &recipe); err != nil {
			t.Fatalf("entry after up: %v", err)
		}
		if amount != "1 l" || recipe != nil {
			t.Errorf("entry after up = %q, %v; want \"1 l\", nil", amount, recipe)
		}

		if err := db.MigrateDownTo(ctx, 3); err != nil {
			t.Fatalf("down again: %v", err)
		}
		if err := db.System().Read().QueryRow(ctx, `SELECT amount_text FROM list_entries WHERE id = ?`, entry).
			Scan(&amount); err != nil {
			t.Fatalf("entry after down: %v", err)
		}
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatalf("up again: %v", err)
		}
	})
}
