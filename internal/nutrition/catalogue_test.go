package nutrition

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestCatalogueFallback: an item linked to a catalogue entry takes its
// values until the household types its own; weights add up per unit, and
// clearing goes back to the catalogue.
func TestCatalogueFallback(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		other := newEnv(t, db, "other")
		e.s.reference = func(key string) (Facts, bool) {
			if key != "garlic" {
				return Facts{}, false
			}
			return Facts{Kcal: 149, Protein: ptr(6.4), Weights: map[string]float64{"clove": 4, "piece": 40}}, true
		}
		rec := e.recipe(t, "Pasta", 2, "2 cloves garlic", "1 onion")
		other.recipe(t, "Pasta", 2, "2 cloves garlic")
		garlic, onion := e.item["garlic"], e.item["onion"]
		link := func(env env, id uuid.UUID, key string) {
			t.Helper()
			err := env.sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
				_, err := q.Exec(ctx, `UPDATE items SET catalog_key = ? WHERE household_id = ? AND id = ?`, key, env.sc.HouseholdID(), id)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		link(e, garlic, "garlic")
		link(e, onion, "onion") // the catalogue has nothing for it
		link(other, other.item["garlic"], "garlic")

		it, err := e.s.Get(ctx, e.sc, garlic)
		if err != nil || it.Set || !it.Catalogue || it.Facts.Kcal != 149 {
			t.Fatalf("garlic from the catalogue: %+v, %v", it, err)
		}
		if it, err := e.s.Get(ctx, e.sc, onion); err != nil || it.Set || it.Catalogue {
			t.Errorf("onion has no catalogue values: %+v, %v", it, err)
		}
		r, err := e.s.Recipe(ctx, e.sc, lines(rec), rec.Servings)
		if err != nil || r.Counted != 1 || r.Of != 2 {
			t.Fatalf("estimate from the catalogue: counted %d of %d, %v", r.Counted, r.Of, err)
		}
		if want := 149 * 8 / 100.0 / 2; r.PerServing.Kcal < want-0.01 || r.PerServing.Kcal > want+0.01 {
			t.Errorf("kcal = %.2f, want %.2f", r.PerServing.Kcal, want)
		}

		// The household's values replace the catalogue's; its clove weight
		// replaces the catalogue's, the piece weight stays.
		if err := e.s.Save(ctx, e.sc, e.by, garlic, Input{Kcal: 100, Weights: map[string]float64{"clove": 5}}); err != nil {
			t.Fatal(err)
		}
		it, err = e.s.Get(ctx, e.sc, garlic)
		if err != nil || !it.Set || !it.Catalogue || it.Facts.Kcal != 100 || it.Facts.Weights["clove"] != 5 || it.Facts.Weights["piece"] != 40 || it.Facts.Protein != nil {
			t.Fatalf("garlic with own values: %+v, %v", it, err)
		}

		if err := e.s.Clear(ctx, e.sc, garlic); err != nil {
			t.Fatal(err)
		}
		it, err = e.s.Get(ctx, e.sc, garlic)
		if err != nil || it.Set || it.Facts.Kcal != 149 || it.Facts.Weights["clove"] != 4 {
			t.Fatalf("garlic after clear: %+v, %v", it, err)
		}

		// The other household's item took the catalogue's values alone.
		other.s.reference = e.s.reference
		if it, err := other.s.Get(ctx, other.sc, other.item["garlic"]); err != nil || it.Set || it.Facts.Kcal != 149 {
			t.Errorf("other household's garlic: %+v, %v", it, err)
		}
	})
}
