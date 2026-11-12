package nutrition

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

type recorder struct {
	mu     sync.Mutex
	events []live.Event
}

func (r *recorder) Publish(_ uuid.UUID, events ...live.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, events...)
}

func (r *recorder) take() []live.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.events
	r.events = nil
	return e
}

type env struct {
	db   *store.DB
	s    *Service
	r    *recipes.Service
	rec  *recorder
	sc   store.Scoped
	hh   storetest.Household
	by   uuid.UUID
	item map[string]uuid.UUID
}

func newEnv(t *testing.T, db *store.DB, name string) env {
	t.Helper()
	rec := &recorder{}
	now := func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }
	hh := storetest.NewHousehold(t, db, name)
	g := groceries.New(rec, now)
	return env{db: db, s: New(rec, now), r: recipes.New(rec, g, now), rec: rec, sc: db.ScopeFor(hh.ID), hh: hh, by: hh.Admin, item: map[string]uuid.UUID{}}
}

// recipe creates a recipe from ingredient lines and remembers its items.
func (e env) recipe(t *testing.T, name string, servings int, lines ...string) recipes.Recipe {
	t.Helper()
	in := recipes.Input{Name: name, Servings: servings}
	for _, l := range lines {
		in.Ingredients = append(in.Ingredients, recipes.ParseIngredient(l, language.English))
	}
	id, err := e.r.Create(t.Context(), e.sc, e.hh.Admin, in)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := e.r.Get(t.Context(), e.sc, id, e.hh.Admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, ing := range rec.Ingredients {
		e.item[strings.ToLower(ing.ItemName)] = ing.ItemID
	}
	e.rec.take()
	return rec
}

func lines(rec recipes.Recipe) []Line {
	out := make([]Line, len(rec.Ingredients))
	for i, ing := range rec.Ingredients {
		out[i] = Line{ItemID: ing.ItemID, Name: ing.ItemName, Amount: ing.Amount, Optional: ing.Optional}
	}
	return out
}

func TestRecipeEstimate(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		rec := e.recipe(t, "Sticky tofu", 2, "400 g tofu", "3 tbsp soy sauce", "3 cloves garlic", "1 thumb ginger")
		tofu, soy, garlic := e.item["tofu"], e.item["soy sauce"], e.item["garlic"]

		r, err := e.s.Recipe(ctx, e.sc, lines(rec), rec.Servings)
		if err != nil {
			t.Fatal(err)
		}
		if r.Show() || r.Counted != 0 || r.Of != 4 {
			t.Fatalf("without values: counted %d of %d, show %v", r.Counted, r.Of, r.Show())
		}

		save := func(id uuid.UUID, in Input) {
			t.Helper()
			if err := e.s.Save(ctx, e.sc, e.by, id, in); err != nil {
				t.Fatal(err)
			}
		}
		save(tofu, Input{Kcal: 144, Protein: ptr(17.3), Fat: ptr(8.7), Carbs: ptr(2.8)})
		save(soy, Input{Kcal: 53, Protein: ptr(8.1), Density: ptr(1.15), SaltG: ptr(13.7)})
		save(garlic, Input{Kcal: 149, Weights: map[string]float64{"clove": 4}})

		r, err = e.s.Recipe(ctx, e.sc, lines(rec), rec.Servings)
		if err != nil {
			t.Fatal(err)
		}
		if r.Counted != 3 || r.Of != 4 || !r.Show() || len(r.Missing) != 1 || r.Missing[0].Reason != ReasonFreeText {
			t.Fatalf("counted %d of %d, show %v, missing %+v", r.Counted, r.Of, r.Show(), r.Missing)
		}
		want := (144*4 + 53*3*14.78676478125*1.15/100 + 149*0.12) / 2
		if d := r.PerServing.Kcal - want; d > 0.01 || d < -0.01 {
			t.Errorf("kcal = %.2f, want %.2f", r.PerServing.Kcal, want)
		}
		// Salt is stored as sodium: 13.7 g salt per 100 g is 5480 mg.
		it, err := e.s.Get(ctx, e.sc, soy)
		if err != nil {
			t.Fatal(err)
		}
		if !it.Set || it.Facts.SodiumMg == nil || *it.Facts.SodiumMg != 13.7*400 || it.Facts.Density == nil || *it.Facts.Density != 1.15 {
			t.Errorf("soy sauce = %+v", it)
		}
		if it.Recipes != 1 {
			t.Errorf("recipes using soy sauce = %d, want 1", it.Recipes)
		}
	})
}

func TestSaveReplacesAndPublishes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		a := e.recipe(t, "A", 2, "2 cloves garlic")
		b := e.recipe(t, "B", 2, "1 clove garlic")
		_ = e.recipe(t, "C", 2, "1 onion")
		garlic := e.item["garlic"]

		if err := e.s.Save(ctx, e.sc, e.by, garlic, Input{Kcal: 149, Weights: map[string]float64{"clove": 4, "piece": 40}}); err != nil {
			t.Fatal(err)
		}
		got := map[uuid.UUID]bool{}
		for _, ev := range e.rec.take() {
			if ev.Kind == live.RecipeChanged {
				got[ev.RecipeID] = true
			}
		}
		if len(got) != 2 || !got[a.ID] || !got[b.ID] {
			t.Errorf("recipe events for %v, want %v and %v only", got, a.ID, b.ID)
		}

		// A second save replaces the first: weights and values.
		if err := e.s.Save(ctx, e.sc, e.by, garlic, Input{Kcal: 150, Weights: map[string]float64{"clove": 5}}); err != nil {
			t.Fatal(err)
		}
		it, err := e.s.Get(ctx, e.sc, garlic)
		if err != nil {
			t.Fatal(err)
		}
		if it.Facts.Kcal != 150 || len(it.Facts.Weights) != 1 || it.Facts.Weights["clove"] != 5 {
			t.Errorf("after second save: %+v", it.Facts)
		}

		if err := e.s.Clear(ctx, e.sc, garlic); err != nil {
			t.Fatal(err)
		}
		it, err = e.s.Get(ctx, e.sc, garlic)
		if err != nil {
			t.Fatal(err)
		}
		if it.Set || len(it.Facts.Weights) != 0 {
			t.Errorf("after clear: %+v", it)
		}
		if err := e.s.Clear(ctx, e.sc, uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Errorf("clear of an unknown item: %v", err)
		}
	})
}

func TestSaveValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		e.recipe(t, "A", 2, "1 onion")
		onion := e.item["onion"]
		for _, c := range []struct {
			name  string
			in    Input
			field string
		}{
			{"kcal too high", Input{Kcal: 950}, "kcal"},
			{"negative", Input{Kcal: 10, Protein: ptr(-1.0)}, "protein"},
			{"macros over 100 g", Input{Kcal: 300, Protein: ptr(14.0), Fat: ptr(9.0), Carbs: ptr(38.0), Fibre: ptr(62.0)}, "fibre"},
			{"saturated over fat", Input{Kcal: 100, Fat: ptr(5.0), SatFat: ptr(8.0)}, "saturated_fat"},
			{"sugars over carbs", Input{Kcal: 100, Carbs: ptr(5.0), Sugars: ptr(8.0)}, "sugars"},
			{"density", Input{Kcal: 10, Density: ptr(0.0)}, "density"},
			{"weight", Input{Kcal: 10, Weights: map[string]float64{"clove": 0}}, "weight.clove"},
			{"weight unit", Input{Kcal: 10, Weights: map[string]float64{"bottle": 5}}, "weight.bottle"},
		} {
			err := e.s.Save(ctx, e.sc, e.by, onion, c.in)
			var fe FieldErrors
			if !errors.As(err, &fe) || fe[c.field] == "" {
				t.Errorf("%s: got %v, want an error on %s", c.name, err, c.field)
			}
		}
		if it, err := e.s.Get(ctx, e.sc, onion); err != nil || it.Set {
			t.Errorf("invalid input was saved: %+v, %v", it, err)
		}
		if err := e.s.Save(ctx, e.sc, e.by, uuid.New(), Input{Kcal: 10}); !errors.Is(err, ErrNotFound) {
			t.Errorf("save for an unknown item: %v", err)
		}
	})
}

// TestHouseholdIsolation walks every Scoped method against two households
// with an item of the same name and checks that neither sees or changes the
// other's rows (docs/plan.md, Multi-tenancy).
func TestHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		a, b := newEnv(t, db, "a"), newEnv(t, db, "b")
		for _, e := range []env{a, b} {
			e.recipe(t, "Dal", 2, "250 g lentils")
			if err := e.s.Save(ctx, e.sc, e.by, e.item["lentils"], Input{Kcal: 353, Weights: map[string]float64{"piece": 1}}); err != nil {
				t.Fatal(err)
			}
		}
		for _, pair := range []struct{ own, other env }{{a, b}, {b, a}} {
			own, other := pair.own, pair.other
			foreign := other.item["lentils"]

			// Reads of the other household's item find nothing.
			if _, err := own.s.Get(ctx, own.sc, foreign); !errors.Is(err, ErrNotFound) {
				t.Errorf("Get of a foreign item: %v", err)
			}
			// The estimate treats a foreign item as having no data.
			r, err := own.s.Recipe(ctx, own.sc, []Line{{ItemID: foreign, Name: "Lentils", Amount: groceries.ParseAmount("100 g", language.English)}}, 1)
			if err != nil || r.Counted != 0 || len(r.Missing) != 1 || r.Missing[0].Reason != ReasonNoData {
				t.Errorf("Recipe with a foreign item: %+v, %v", r, err)
			}
			// Writes to the other household's item are refused and change nothing.
			if err := own.s.Save(ctx, own.sc, own.by, foreign, Input{Kcal: 1}); !errors.Is(err, ErrNotFound) {
				t.Errorf("Save on a foreign item: %v", err)
			}
			if err := own.s.Clear(ctx, own.sc, foreign); !errors.Is(err, ErrNotFound) {
				t.Errorf("Clear on a foreign item: %v", err)
			}
			it, err := other.s.Get(ctx, other.sc, foreign)
			if err != nil || !it.Set || it.Facts.Kcal != 353 || len(it.Facts.Weights) != 1 {
				t.Errorf("foreign item changed: %+v, %v", it, err)
			}
		}
	})
}
