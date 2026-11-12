package plan

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
)

// StaleAfter is "a while" for "haven't cooked in a while": recipes last
// cooked longer ago than this, or never, are suggested.
const StaleAfter = 21 * 24 * time.Hour

// Suggestion is a recipe the plan suggests.
type Suggestion struct {
	Recipe Recipe
	// LastCookedAt is nil for a recipe never cooked.
	LastCookedAt *time.Time
	createdAt    time.Time
}

// Suggestions returns up to limit recipes not cooked within StaleAfter and
// not planned from from up to (not including) to: those cooked longest ago
// first, then the ones never cooked, oldest first. Recipes that have been
// cooked come first because they are known to be liked; new ones follow.
func (s *Service) Suggestions(ctx context.Context, sc store.Scoped, from, to store.Date, limit int) ([]Suggestion, error) {
	var rows []struct {
		ID           uuid.UUID
		Slug         string
		Name         string
		Photo        *string
		PrepMin      *int
		CookMin      *int
		Servings     int
		CreatedAt    store.Time
		LastCookedAt *store.Time
	}
	err := sc.Read().Select(ctx, &rows, `SELECT r.id, r.slug, r.name, r.photo, r.prep_min, r.cook_min, r.servings, r.created_at,
			(SELECT max(ce.cooked_at) FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id) AS last_cooked_at
		FROM recipes r
		WHERE r.household_id = ? AND r.deleted_at IS NULL AND r.draft = ?
			AND NOT EXISTS (SELECT 1 FROM cook_events ce WHERE ce.household_id = r.household_id AND ce.recipe_id = r.id
				AND ce.cooked_at >= ?)
			AND NOT EXISTS (SELECT 1 FROM planned_meals pm WHERE pm.household_id = r.household_id AND pm.recipe_id = r.id
				AND pm.deleted_at IS NULL AND pm.date >= ? AND pm.date < ?)`,
		sc.HouseholdID(), false, store.T(s.now().Add(-StaleAfter)), from, to)
	if err != nil {
		return nil, err
	}
	out := make([]Suggestion, len(rows))
	for i, r := range rows {
		out[i] = Suggestion{
			Recipe:    Recipe{ID: r.ID, Slug: r.Slug, Name: r.Name, TotalMin: totalMin(r.PrepMin, r.CookMin), Servings: r.Servings},
			createdAt: r.CreatedAt.Time,
		}
		if r.Photo != nil {
			out[i].Recipe.Photo = *r.Photo
		}
		if r.LastCookedAt != nil {
			t := r.LastCookedAt.Time
			out[i].LastCookedAt = &t
		}
	}
	slices.SortFunc(out, func(a, b Suggestion) int {
		switch {
		case a.LastCookedAt != nil && b.LastCookedAt != nil:
			if c := a.LastCookedAt.Compare(*b.LastCookedAt); c != 0 {
				return c
			}
		case a.LastCookedAt != nil:
			return -1
		case b.LastCookedAt != nil:
			return 1
		}
		if c := a.createdAt.Compare(b.createdAt); c != 0 {
			return c
		}
		return cmp.Compare(a.Recipe.ID.String(), b.Recipe.ID.String())
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Tonight is what the library suggests cooking tonight: today's first
// planned meal, or else the first suggestion not planned this week.
type Tonight struct {
	Meal       *Meal
	Suggestion *Suggestion
}

// Tonight returns the suggestion for tonight; both fields are nil when the
// library has nothing to suggest.
func (s *Service) Tonight(ctx context.Context, sc store.Scoped) (Tonight, error) {
	c, err := s.Calendar(ctx, sc)
	if err != nil {
		return Tonight{}, err
	}
	today := c.Today(s.now())
	meals, err := between(ctx, sc.Read(), sc, today, today.AddDays(1))
	if err != nil {
		return Tonight{}, err
	}
	if len(meals) > 0 {
		return Tonight{Meal: &meals[0]}, nil
	}
	week := c.WeekOf(today)
	sugg, err := s.Suggestions(ctx, sc, week, week.AddDays(7), 1)
	if err != nil || len(sugg) == 0 {
		return Tonight{}, err
	}
	return Tonight{Suggestion: &sugg[0]}, nil
}

func cookEvents(recipeID uuid.UUID) []live.Event {
	return []live.Event{{Kind: live.RecipeChanged, RecipeID: recipeID}, {Kind: live.RecipesChanged}, planChanged}
}

// MarkCooked records that the member by cooked the recipe now ("I made
// this") and returns the cook event, for Undo with UndoCooked.
func (s *Service) MarkCooked(ctx context.Context, sc store.Scoped, recipeID, by uuid.UUID) (uuid.UUID, error) {
	id := store.NewID()
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if _, err := lockRecipe(ctx, q, sc, recipeID); err != nil {
			return err
		}
		now := s.clock()
		_, err := q.Exec(ctx, `INSERT INTO cook_events (id, household_id, recipe_id, user_id, cooked_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, id, sc.HouseholdID(), recipeID, by, now, now, now)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.publish(sc, cookEvents(recipeID)...)
	return id, nil
}

// UndoCooked deletes a cook event of the recipe recorded within
// UndoWindow, or returns ErrNotFound.
func (s *Service) UndoCooked(ctx context.Context, sc store.Scoped, recipeID, eventID uuid.UUID) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM cook_events WHERE household_id = ? AND recipe_id = ? AND id = ? AND created_at >= ?`,
			sc.HouseholdID(), recipeID, eventID, s.undoCutoff())
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil {
		return err
	}
	s.publish(sc, cookEvents(recipeID)...)
	return nil
}
