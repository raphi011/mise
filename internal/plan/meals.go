package plan

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
)

// Meal is a planned meal with what the plan shows of its recipe.
type Meal struct {
	ID       uuid.UUID
	Date     store.Date
	Position int
	// Servings is what this meal is planned for; the recipe makes
	// Recipe.Servings.
	Servings int
	Recipe   Recipe
}

// Recipe is a planned recipe as the plan shows it.
type Recipe struct {
	ID uuid.UUID
	// Slug is the recipe's address, see recipes.Slugify.
	Slug     string
	Name     string
	Photo    string
	TotalMin *int
	Servings int
}

// Day is one date with its meals in order.
type Day struct {
	Date  store.Date
	Meals []Meal
}

// Week is seven days from the household's first day of the week.
type Week struct {
	Start store.Date
	Days  []Day
	Today store.Date
}

// Month is the weeks that cover a calendar month, each starting on the
// household's first day of the week; days of the neighbouring months fill
// the first and last week.
type Month struct {
	Year  int
	Month time.Month
	Weeks [][]Day
	Today store.Date
}

type mealRow struct {
	ID             uuid.UUID
	Date           store.Date
	Position       int
	Servings       int
	RecipeID       uuid.UUID
	RecipeSlug     string
	Name           string
	Photo          *string
	PrepMin        *int
	CookMin        *int
	RecipeServings int
}

func (r mealRow) meal() Meal {
	m := Meal{ID: r.ID, Date: r.Date, Position: r.Position, Servings: r.Servings,
		Recipe: Recipe{ID: r.RecipeID, Slug: r.RecipeSlug, Name: r.Name, TotalMin: totalMin(r.PrepMin, r.CookMin), Servings: r.RecipeServings}}
	if r.Photo != nil {
		m.Recipe.Photo = *r.Photo
	}
	return m
}

func totalMin(prep, cook *int) *int {
	if prep == nil && cook == nil {
		return nil
	}
	t := 0
	if prep != nil {
		t += *prep
	}
	if cook != nil {
		t += *cook
	}
	return &t
}

// mealSelect selects mealRow columns of live meals whose recipe is live:
// a meal of a deleted recipe is hidden until the recipe is restored. Its
// first argument is the household; callers append conditions on pm.
const mealSelect = `SELECT pm.id, pm.date, pm.position, pm.servings, r.id AS recipe_id, r.slug AS recipe_slug, r.name, r.photo,
		r.prep_min, r.cook_min, r.servings AS recipe_servings
	FROM planned_meals pm JOIN recipes r ON r.household_id = pm.household_id AND r.id = pm.recipe_id
	WHERE pm.household_id = ? AND pm.deleted_at IS NULL AND r.deleted_at IS NULL`

const mealOrder = ` ORDER BY pm.date, pm.position, pm.created_at, pm.id`

// between returns the meals on from up to (not including) to, in order.
func between(ctx context.Context, q store.Reader, sc store.Scoped, from, to store.Date) ([]Meal, error) {
	var rows []mealRow
	if err := q.Select(ctx, &rows, mealSelect+` AND pm.date >= ? AND pm.date < ?`+mealOrder,
		sc.HouseholdID(), from, to); err != nil {
		return nil, err
	}
	out := make([]Meal, len(rows))
	for i, r := range rows {
		out[i] = r.meal()
	}
	return out, nil
}

// days returns n days from start with their meals.
func days(ctx context.Context, q store.Reader, sc store.Scoped, start store.Date, n int) ([]Day, error) {
	meals, err := between(ctx, q, sc, start, start.AddDays(n))
	if err != nil {
		return nil, err
	}
	out := make([]Day, n)
	idx := make(map[store.Date]int, n)
	for i := range out {
		out[i].Date = start.AddDays(i)
		idx[out[i].Date] = i
	}
	for _, m := range meals {
		i := idx[m.Date]
		out[i].Meals = append(out[i].Meals, m)
	}
	return out, nil
}

// Week returns the week that contains date.
func (s *Service) Week(ctx context.Context, sc store.Scoped, date store.Date) (Week, error) {
	c, err := s.Calendar(ctx, sc)
	if err != nil {
		return Week{}, err
	}
	w := Week{Start: c.WeekOf(date), Today: c.Today(s.now())}
	w.Days, err = days(ctx, sc.Read(), sc, w.Start, 7)
	return w, err
}

// Month returns the weeks that cover the calendar month.
func (s *Service) Month(ctx context.Context, sc store.Scoped, year int, month time.Month) (Month, error) {
	c, err := s.Calendar(ctx, sc)
	if err != nil {
		return Month{}, err
	}
	first := store.DateOf(time.Date(year, month, 1, 12, 0, 0, 0, time.UTC))
	last := store.DateOf(time.Date(year, month+1, 0, 12, 0, 0, 0, time.UTC))
	start, end := c.WeekOf(first), c.WeekOf(last).AddDays(7)
	n := 0
	for d := start; d.Before(end); d = d.AddDays(1) {
		n++
	}
	all, err := days(ctx, sc.Read(), sc, start, n)
	if err != nil {
		return Month{}, err
	}
	m := Month{Year: first.Year, Month: first.Month, Today: c.Today(s.now())}
	for i := 0; i < len(all); i += 7 {
		m.Weeks = append(m.Weeks, all[i:i+7])
	}
	return m, nil
}

// Get returns a planned meal, or ErrNotFound.
func (s *Service) Get(ctx context.Context, sc store.Scoped, id uuid.UUID) (Meal, error) {
	return getMeal(ctx, sc.Read(), sc, id)
}

func getMeal(ctx context.Context, q store.Reader, sc store.Scoped, id uuid.UUID) (Meal, error) {
	var r mealRow
	if err := q.Get(ctx, &r, mealSelect+` AND pm.id = ?`, sc.HouseholdID(), id); err != nil {
		return Meal{}, notFound(err)
	}
	return r.meal(), nil
}

// PlannedFor returns the dates from today on for which the recipe is
// planned, for the delete confirmation.
func (s *Service) PlannedFor(ctx context.Context, sc store.Scoped, recipeID uuid.UUID) ([]store.Date, error) {
	today, err := s.Today(ctx, sc)
	if err != nil {
		return nil, err
	}
	var dates []store.Date
	err = sc.Read().Select(ctx, &dates, `SELECT pm.date FROM planned_meals pm
		WHERE pm.household_id = ? AND pm.recipe_id = ? AND pm.deleted_at IS NULL AND pm.date >= ?
		ORDER BY pm.date, pm.position`, sc.HouseholdID(), recipeID, today)
	return dates, err
}

// MealInput is one recipe to plan.
type MealInput struct {
	RecipeID uuid.UUID
	// Servings defaults to the recipe's when zero.
	Servings int
	// Ingredients are IDs of the recipe's ingredients to put on the
	// default list, scaled to the servings. Items already on it stay as
	// they are.
	Ingredients []uuid.UUID
	// Also are other recipes, such as linked side dishes, to plan right
	// after this meal at the same servings. Recipes that are gone, or
	// planned by this add already, are skipped.
	Also []uuid.UUID
}

// AddInput is one or more meals to plan on a day.
type AddInput struct {
	Date store.Date
	// Meals go after the meals already on the day, in order. A recipe
	// that comes up a second time is skipped.
	Meals []MealInput
	// By is the acting member.
	By   uuid.UUID
	Lang language.Tag
}

// Added describes what Add did.
type Added struct {
	// Meals are the meals planned for AddInput.Meals, in order.
	Meals []Meal
	// Also are the meals planned for their Also.
	Also     []Meal
	Shopping recipes.PutOn
}

// All returns every meal Add planned, in their order on the day.
func (a Added) All() []Meal {
	out := slices.Concat(a.Meals, a.Also)
	slices.SortFunc(out, func(x, y Meal) int { return x.Position - y.Position })
	return out
}

// Add plans recipes for a day, after the meals already on it, and puts
// the chosen ingredients on the default list in the same transaction. It
// returns ErrNotFound, and changes nothing, when one of AddInput.Meals is
// gone.
func (s *Service) Add(ctx context.Context, sc store.Scoped, in AddInput) (Added, error) {
	errs := FieldErrors{}
	if in.Date.IsZero() {
		errs["date"] = "form.error.required"
	}
	if len(in.Meals) == 0 {
		errs["recipe"] = "form.error.required"
	}
	for _, m := range in.Meals {
		if m.Servings < 0 || m.Servings > MaxServings {
			errs["servings"] = "form.error.range"
		}
	}
	if len(errs) > 0 {
		return Added{}, errs
	}
	// A linked recipe that is also one of the meals is planned as that
	// meal, with its own servings and ingredients.
	mains := make(map[uuid.UUID]bool, len(in.Meals))
	for _, m := range in.Meals {
		mains[m.RecipeID] = true
	}
	var out Added
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		pos, err := nextPosition(ctx, q, sc, in.Date)
		if err != nil {
			return err
		}
		now := s.clock()
		planned := map[uuid.UUID]bool{}
		insert := func(recipe uuid.UUID, servings int) (Meal, error) {
			id := store.NewID()
			if _, err := q.Exec(ctx, `INSERT INTO planned_meals (id, household_id, date, recipe_id, servings, position,
					created_by, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, sc.HouseholdID(), in.Date, recipe, servings, pos, in.By, now, now); err != nil {
				return Meal{}, err
			}
			pos++
			planned[recipe] = true
			return getMeal(ctx, q.Reader, sc, id)
		}
		for _, mi := range in.Meals {
			if planned[mi.RecipeID] {
				continue
			}
			base, err := lockRecipe(ctx, q, sc, mi.RecipeID)
			if err != nil {
				return err
			}
			if mi.Servings == 0 {
				mi.Servings = base
			}
			m, err := insert(mi.RecipeID, mi.Servings)
			if err != nil {
				return err
			}
			out.Meals = append(out.Meals, m)
			for _, r := range mi.Also {
				if planned[r] || mains[r] {
					continue
				}
				if _, err := lockRecipe(ctx, q, sc, r); errors.Is(err, ErrNotFound) {
					continue
				} else if err != nil {
					return err
				}
				m, err := insert(r, mi.Servings)
				if err != nil {
					return err
				}
				out.Also = append(out.Also, m)
			}
			if len(mi.Ingredients) > 0 {
				put, err := s.recipes.PutOnListTx(ctx, q, sc, in.By, mi.RecipeID, mi.Ingredients, mi.Servings, in.Lang)
				if err != nil {
					return notFoundFrom(err)
				}
				out.Shopping.List = put.List
				out.Shopping.Added = append(out.Shopping.Added, put.Added...)
				out.Shopping.Present = append(out.Shopping.Present, put.Present...)
			}
		}
		return nil
	})
	if err != nil {
		return Added{}, err
	}
	events := []live.Event{planChanged}
	if len(out.Shopping.Added) > 0 {
		events = append(events, live.Event{Kind: live.ListChanged, ListID: out.Shopping.List.ID})
	}
	s.publish(sc, events...)
	return out, nil
}

// notFoundFrom maps the not-found errors of the recipes and groceries
// services to ErrNotFound.
func notFoundFrom(err error) error {
	if errors.Is(err, recipes.ErrNotFound) || errors.Is(err, groceries.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// nextPosition is the position after the last live meal of the day.
func nextPosition(ctx context.Context, q store.Querier, sc store.Scoped, date store.Date) (int, error) {
	var pos int
	err := q.QueryRow(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM planned_meals
		WHERE household_id = ? AND date = ? AND deleted_at IS NULL`, sc.HouseholdID(), date).Scan(&pos)
	return pos, err
}

// Placement puts a meal on a day at a position. Moves and swaps return
// the placements that undo them.
type Placement struct {
	Meal     uuid.UUID
	Date     store.Date
	Position int
}

// Change describes a move or swap.
type Change struct {
	// Meal is the moved meal and From the day it was on.
	Meal Meal
	From store.Date
	// With is the meal it swapped days with, if any.
	With *Meal
	// Undo puts every meal back; empty when nothing changed.
	Undo []Placement
}

// Move puts a meal on another day, after the meals already on it.
func (s *Service) Move(ctx context.Context, sc store.Scoped, id uuid.UUID, to store.Date) (Change, error) {
	return s.rearrange(ctx, sc, id, to, false)
}

// Swap puts a meal on another day in place of that day's first meal, which
// takes the meal's old day and position; the day's other meals stay. On a
// day without meals it is a Move.
func (s *Service) Swap(ctx context.Context, sc store.Scoped, id uuid.UUID, to store.Date) (Change, error) {
	return s.rearrange(ctx, sc, id, to, true)
}

func (s *Service) rearrange(ctx context.Context, sc store.Scoped, id uuid.UUID, to store.Date, swap bool) (Change, error) {
	if to.IsZero() {
		return Change{}, FieldErrors{"date": "form.error.required"}
	}
	var out Change
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		m, err := getMeal(ctx, q.Reader, sc, id)
		if err != nil {
			return err
		}
		out.Meal, out.From = m, m.Date
		if m.Date == to {
			return nil
		}
		now := s.clock()
		place := func(meal uuid.UUID, date store.Date, pos int) error {
			_, err := q.Exec(ctx, `UPDATE planned_meals SET date = ?, position = ?, updated_at = ?
				WHERE household_id = ? AND id = ?`, date, pos, now, sc.HouseholdID(), meal)
			return err
		}
		var first []mealRow
		if swap {
			if err := q.Select(ctx, &first, mealSelect+` AND pm.date = ?`+mealOrder+` LIMIT 1`, sc.HouseholdID(), to); err != nil {
				return err
			}
		}
		out.Undo = []Placement{{Meal: m.ID, Date: m.Date, Position: m.Position}}
		if len(first) == 1 {
			other := first[0].meal()
			out.Undo = append(out.Undo, Placement{Meal: other.ID, Date: other.Date, Position: other.Position})
			if err := place(m.ID, to, other.Position); err != nil {
				return err
			}
			if err := place(other.ID, m.Date, m.Position); err != nil {
				return err
			}
			if other, err = getMeal(ctx, q.Reader, sc, other.ID); err != nil {
				return err
			}
			out.With = &other
		} else {
			pos, err := nextPosition(ctx, q, sc, to)
			if err != nil {
				return err
			}
			if err := place(m.ID, to, pos); err != nil {
				return err
			}
		}
		out.Meal, err = getMeal(ctx, q.Reader, sc, m.ID)
		return err
	})
	if err != nil {
		return Change{}, err
	}
	if len(out.Undo) > 0 {
		s.publish(sc, planChanged)
	}
	return out, nil
}

// Place puts meals on the given days and positions: the Undo of a move or
// swap. It returns ErrNotFound, and changes nothing, when one of them was
// removed in the meantime.
func (s *Service) Place(ctx context.Context, sc store.Scoped, placements []Placement) error {
	for _, p := range placements {
		if p.Date.IsZero() {
			return FieldErrors{"date": "form.error.required"}
		}
	}
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		now := s.clock()
		for _, p := range placements {
			res, err := q.Exec(ctx, `UPDATE planned_meals SET date = ?, position = ?, updated_at = ?
				WHERE household_id = ? AND id = ? AND deleted_at IS NULL`, p.Date, p.Position, now, sc.HouseholdID(), p.Meal)
			if err != nil {
				return err
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(sc, planChanged)
	return nil
}

// Moved describes a MoveAll.
type Moved struct {
	// Meals are the meals moved, in their new order.
	Meals []Meal
	// Undo puts every moved meal back; empty when nothing changed.
	Undo []Placement
}

// MoveAll puts meals on another day, after the meals already on it, in
// the order they had on the plan. Meals already on that day stay where
// they are and meals that are gone are skipped; it returns ErrNotFound
// when none of them is left.
func (s *Service) MoveAll(ctx context.Context, sc store.Scoped, ids []uuid.UUID, to store.Date) (Moved, error) {
	if to.IsZero() {
		return Moved{}, FieldErrors{"date": "form.error.required"}
	}
	var out Moved
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		meals, err := liveMeals(ctx, q.Reader, sc, ids)
		if err != nil {
			return err
		}
		pos, err := nextPosition(ctx, q, sc, to)
		if err != nil {
			return err
		}
		now := s.clock()
		for _, m := range meals {
			if m.Date == to {
				continue
			}
			if _, err := q.Exec(ctx, `UPDATE planned_meals SET date = ?, position = ?, updated_at = ?
				WHERE household_id = ? AND id = ?`, to, pos, now, sc.HouseholdID(), m.ID); err != nil {
				return err
			}
			pos++
			out.Undo = append(out.Undo, Placement{Meal: m.ID, Date: m.Date, Position: m.Position})
			moved, err := getMeal(ctx, q.Reader, sc, m.ID)
			if err != nil {
				return err
			}
			out.Meals = append(out.Meals, moved)
		}
		return nil
	})
	if err != nil {
		return Moved{}, err
	}
	if len(out.Undo) > 0 {
		s.publish(sc, planChanged)
	}
	return out, nil
}

// liveMeals returns the meals of ids that are still planned, in their
// order on the plan, or ErrNotFound when none is.
func liveMeals(ctx context.Context, q store.Reader, sc store.Scoped, ids []uuid.UUID) ([]Meal, error) {
	var out []Meal
	for _, id := range ids {
		if slices.ContainsFunc(out, func(m Meal) bool { return m.ID == id }) {
			continue
		}
		m, err := getMeal(ctx, q, sc, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	slices.SortFunc(out, func(a, b Meal) int {
		if c := a.Date.Compare(b.Date); c != 0 {
			return c
		}
		return a.Position - b.Position
	})
	return out, nil
}

// SetServings changes what a meal is planned for. Amounts already on a
// list stay as they are. It returns the meal's
// previous servings.
func (s *Service) SetServings(ctx context.Context, sc store.Scoped, id uuid.UUID, servings int) (int, error) {
	if servings < 1 || servings > MaxServings {
		return 0, FieldErrors{"servings": "form.error.range"}
	}
	var old int
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		m, err := getMeal(ctx, q.Reader, sc, id)
		if err != nil {
			return err
		}
		old = m.Servings
		_, err = q.Exec(ctx, `UPDATE planned_meals SET servings = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			servings, s.clock(), sc.HouseholdID(), id)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.publish(sc, planChanged)
	return old, nil
}

// Replace plans another recipe for a meal, keeping its day, position and
// servings. It returns the recipe it replaced, for Undo.
func (s *Service) Replace(ctx context.Context, sc store.Scoped, id, recipeID uuid.UUID) (uuid.UUID, error) {
	var old uuid.UUID
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		m, err := getMeal(ctx, q.Reader, sc, id)
		if err != nil {
			return err
		}
		if _, err := lockRecipe(ctx, q, sc, recipeID); err != nil {
			return err
		}
		old = m.Recipe.ID
		_, err = q.Exec(ctx, `UPDATE planned_meals SET recipe_id = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
			recipeID, s.clock(), sc.HouseholdID(), id)
		return err
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.publish(sc, planChanged)
	return old, nil
}

// Remove takes a meal off the plan. Undo with Restore within UndoWindow;
// the sweep deletes it for good afterwards.
func (s *Service) Remove(ctx context.Context, sc store.Scoped, id uuid.UUID) (Meal, error) {
	ms, err := s.RemoveAll(ctx, sc, []uuid.UUID{id})
	if err != nil {
		return Meal{}, err
	}
	return ms[0], nil
}

// RemoveAll takes meals off the plan, skipping meals that are gone, and
// returns the removed meals in their order on the plan; ErrNotFound when
// none was left. Undo with RestoreAll within UndoWindow.
func (s *Service) RemoveAll(ctx context.Context, sc store.Scoped, ids []uuid.UUID) ([]Meal, error) {
	var out []Meal
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		meals, err := liveMeals(ctx, q.Reader, sc, ids)
		if err != nil {
			return err
		}
		for _, m := range meals {
			if _, err := s.removeTx(ctx, q, sc, m.ID); err != nil {
				return err
			}
		}
		out = meals
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.publish(sc, planChanged)
	return out, nil
}

// Unplan is the Undo of Add: it removes the meal and the other meals
// planned with it (also; ones already gone are skipped) and takes back
// the entries planning them put on the list, in one transaction. Only
// entries of a removed meal's recipe are taken back, and entries changed
// since stay on the list (groceries TakeBackTx).
func (s *Service) Unplan(ctx context.Context, sc store.Scoped, id uuid.UUID, entries, also []uuid.UUID) (Meal, error) {
	var (
		m     Meal
		lists []uuid.UUID
	)
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var err error
		if m, err = s.removeTx(ctx, q, sc, id); err != nil {
			return err
		}
		recipeIDs := []uuid.UUID{m.Recipe.ID}
		for _, a := range also {
			if a == id {
				continue
			}
			other, err := s.removeTx(ctx, q, sc, a)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if !slices.Contains(recipeIDs, other.Recipe.ID) {
				recipeIDs = append(recipeIDs, other.Recipe.ID)
			}
		}
		if len(entries) == 0 {
			return nil
		}
		for _, r := range recipeIDs {
			ls, err := s.recipes.TakeOffListTx(ctx, q, sc, r, entries)
			if err != nil {
				return err
			}
			for _, l := range ls {
				if !slices.Contains(lists, l) {
					lists = append(lists, l)
				}
			}
		}
		return nil
	})
	if err != nil {
		return Meal{}, err
	}
	events := []live.Event{planChanged}
	for _, l := range lists {
		events = append(events, live.Event{Kind: live.ListChanged, ListID: l})
	}
	s.publish(sc, events...)
	return m, nil
}

func (s *Service) removeTx(ctx context.Context, q store.Querier, sc store.Scoped, id uuid.UUID) (Meal, error) {
	m, err := getMeal(ctx, q.Reader, sc, id)
	if err != nil {
		return Meal{}, err
	}
	now := s.clock()
	_, err = q.Exec(ctx, `UPDATE planned_meals SET deleted_at = ?, updated_at = ? WHERE household_id = ? AND id = ?`,
		now, now, sc.HouseholdID(), id)
	return m, err
}

// Restore undoes Remove within UndoWindow, or returns ErrNotFound.
func (s *Service) Restore(ctx context.Context, sc store.Scoped, id uuid.UUID) error {
	return s.RestoreAll(ctx, sc, []uuid.UUID{id})
}

// RestoreAll undoes RemoveAll within UndoWindow. Meals past it are
// skipped; it returns ErrNotFound when none could be restored.
func (s *Service) RestoreAll(ctx context.Context, sc store.Scoped, ids []uuid.UUID) error {
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		restored := false
		for _, id := range ids {
			res, err := q.Exec(ctx, `UPDATE planned_meals SET deleted_at = NULL, updated_at = ?
				WHERE household_id = ? AND id = ? AND deleted_at >= ?`, s.clock(), sc.HouseholdID(), id, s.undoCutoff())
			if err != nil {
				return err
			}
			if err := mustAffect(res); err == nil {
				restored = true
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		if !restored {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.publish(sc, planChanged)
	return nil
}
