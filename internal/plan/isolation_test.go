package plan

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdIsolation walks every Scoped method of this package against
// two households with identical content and checks that neither sees or
// changes the other's rows.
func TestHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		type fixture struct {
			env
			recipe  recipes.Recipe
			meal    Meal
			removed Meal
			// entry is on the list from planning meal.
			entry  uuid.UUID
			cooked uuid.UUID
			// cookedRecipe is the recipe of cooked.
			cookedRecipe uuid.UUID
		}
		setup := func(name string) fixture {
			e := newEnv(t, db, name)
			f := fixture{env: e, recipe: e.recipe(t, "Dal", "250 g red lentils")}
			added, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: f.recipe.ID, Date: date("2026-11-10"), By: e.hh.Admin,
				Ingredients: []uuid.UUID{f.recipe.Ingredients[0].ID}, Lang: language.English})
			if err != nil {
				t.Fatal(err)
			}
			f.meal, f.entry = added.Meal, added.Shopping.Added[0].ID
			f.removed = e.add(t, f.recipe, "2026-11-11")
			if _, err := e.s.Remove(ctx, e.sc, f.removed.ID); err != nil {
				t.Fatal(err)
			}
			// Dal was cooked long ago, so it is suggested; soup just now.
			e.cooked(t, f.recipe.ID, start.Add(-30*24*time.Hour))
			f.cookedRecipe = e.recipe(t, "Soup").ID
			if f.cooked, err = e.s.MarkCooked(ctx, e.sc, f.cookedRecipe, e.hh.Admin); err != nil {
				t.Fatal(err)
			}
			return f
		}
		a, b := setup("a"), setup("b")

		notFound := func(what string, err error) {
			t.Helper()
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: %v, want ErrNotFound", what, err)
			}
		}

		for _, pair := range []struct{ own, other fixture }{{a, b}, {b, a}} {
			own, other := pair.own, pair.other
			s, sc, me := own.s, own.sc, own.hh.Admin
			day := date("2026-11-10")

			// Reads return only own rows.
			if w, err := s.Week(ctx, sc, day); err != nil || len(w.Days[1].Meals) != 1 || w.Days[1].Meals[0].ID != own.meal.ID {
				t.Errorf("week: %+v, %v", w.Days[1], err)
			}
			if m, err := s.Month(ctx, sc, 2026, time.November); err != nil || len(m.Weeks[2][1].Meals) != 1 ||
				m.Weeks[2][1].Meals[0].ID != own.meal.ID {
				t.Errorf("month: %+v, %v", m.Weeks[2][1], err)
			}
			if dates, err := s.PlannedFor(ctx, sc, other.recipe.ID); err != nil || len(dates) != 0 {
				t.Errorf("planned for other's recipe: %v, %v", dates, err)
			}
			if sugg, err := s.Suggestions(ctx, sc, date("2026-11-16"), date("2026-11-23"), 10); err != nil ||
				len(sugg) != 1 || sugg[0].Recipe.ID != own.recipe.ID {
				t.Errorf("suggestions: %+v, %v", sugg, err)
			}
			// Dal is planned this week, and Soup was just cooked.
			if tn, err := s.Tonight(ctx, sc); err != nil || tn.Meal != nil || tn.Suggestion != nil {
				t.Errorf("tonight: %+v, %v", tn, err)
			}
			_, err := s.Get(ctx, sc, other.meal.ID)
			notFound("get other's meal", err)

			// Writes to the other's rows fail and leave them alone.
			_, err = s.Add(ctx, sc, AddInput{RecipeID: other.recipe.ID, Date: day, By: me, Lang: language.English})
			notFound("plan other's recipe", err)
			_, err = s.Add(ctx, sc, AddInput{RecipeID: own.recipe.ID, Date: day, By: me, Lang: language.English,
				Ingredients: []uuid.UUID{other.recipe.Ingredients[0].ID}})
			notFound("put other's ingredient on the list", err)
			_, err = s.Move(ctx, sc, other.meal.ID, day.AddDays(1))
			notFound("move", err)
			_, err = s.Swap(ctx, sc, other.meal.ID, day.AddDays(1))
			notFound("swap", err)
			notFound("place", s.Place(ctx, sc, []Placement{{Meal: other.meal.ID, Date: day.AddDays(3)}}))
			_, err = s.SetServings(ctx, sc, other.meal.ID, 6)
			notFound("servings", err)
			_, err = s.Replace(ctx, sc, other.meal.ID, own.recipe.ID)
			notFound("replace other's meal", err)
			_, err = s.Replace(ctx, sc, own.meal.ID, other.recipe.ID)
			notFound("replace with other's recipe", err)
			_, err = s.Remove(ctx, sc, other.meal.ID)
			notFound("remove", err)
			notFound("restore", s.Restore(ctx, sc, other.removed.ID))
			_, err = s.Unplan(ctx, sc, other.meal.ID, nil, nil)
			notFound("unplan other's meal", err)
			// Undo of planning an own meal leaves the other's entry alone.
			mine := own.add(t, own.recipe, "2026-11-13")
			if _, err := s.Unplan(ctx, sc, mine.ID, []uuid.UUID{other.entry}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := other.g.Entry(ctx, other.sc, other.entry); err != nil {
				t.Errorf("unplan took the other's entry: %v", err)
			}
			_, err = s.MarkCooked(ctx, sc, other.recipe.ID, me)
			notFound("cook other's recipe", err)
			notFound("undo other's cook event", s.UndoCooked(ctx, sc, other.cookedRecipe, other.cooked))

			// Swapping onto a day only considers own meals: own meal moves
			// alone.
			ch, err := s.Swap(ctx, sc, own.meal.ID, date("2026-11-12"))
			if err != nil || ch.With != nil {
				t.Errorf("swap onto a day free in own plan: %+v, %v", ch, err)
			}
			if err := s.Place(ctx, sc, ch.Undo); err != nil {
				t.Fatal(err)
			}

			got, err := other.s.Get(ctx, other.sc, other.meal.ID)
			if err != nil || got.Date != day || got.Servings != 2 || got.Recipe.ID != other.recipe.ID {
				t.Errorf("other's meal changed: %+v, %v", got, err)
			}
			if r, err := other.r.Get(ctx, other.sc, other.recipe.ID, other.hh.Admin); err != nil || r.TimesCooked != 1 {
				t.Errorf("other's cook history changed: %d, %v", r.TimesCooked, err)
			}
		}
	})
}
