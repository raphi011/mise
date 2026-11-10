package plan

import (
	"context"
	"errors"
	"slices"
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

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	db   *store.DB
	s    *Service
	r    *recipes.Service
	g    *groceries.Service
	rec  *recorder
	clk  *clock
	sc   store.Scoped
	hh   storetest.Household
	list groceries.List
}

// start is "now" in tests: Monday 9 November 2026, noon UTC.
var start = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func newEnv(t *testing.T, db *store.DB, name string) env {
	t.Helper()
	rec := &recorder{}
	clk := &clock{t: start}
	hh := storetest.NewHousehold(t, db, name)
	g := groceries.New(rec, clk.Now)
	r := recipes.New(rec, g, clk.Now)
	e := env{db: db, s: New(rec, r, clk.Now), r: r, g: g, rec: rec, clk: clk, sc: db.ScopeFor(hh.ID), hh: hh}
	var err error
	if e.list, err = g.Start(t.Context(), e.sc, groceries.Defaults{ListName: "Weekly shop"}); err != nil {
		t.Fatal(err)
	}
	rec.take()
	return e
}

func date(s string) store.Date {
	d, err := store.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func (e env) recipe(t *testing.T, name string, lines ...string) recipes.Recipe {
	t.Helper()
	in := recipes.Input{Name: name, Servings: 2}
	for _, l := range lines {
		in.Ingredients = append(in.Ingredients, recipes.ParseIngredient(l, language.English))
	}
	id, err := e.r.Create(t.Context(), e.sc, e.hh.Admin, in)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.r.Get(t.Context(), e.sc, id, e.hh.Admin)
	if err != nil {
		t.Fatal(err)
	}
	e.rec.take()
	return r
}

func (e env) add(t *testing.T, r recipes.Recipe, day string) Meal {
	t.Helper()
	a, err := e.s.Add(t.Context(), e.sc, AddInput{RecipeID: r.ID, Date: date(day), By: e.hh.Admin, Lang: language.English})
	if err != nil {
		t.Fatalf("plan %s on %s: %v", r.Name, day, err)
	}
	return a.Meal
}

// dayNames lists the recipe names per day of the week.
func (e env) dayNames(t *testing.T, day string) [][]string {
	t.Helper()
	w, err := e.s.Week(t.Context(), e.sc, date(day))
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]string, len(w.Days))
	for i, d := range w.Days {
		for _, m := range d.Meals {
			out[i] = append(out[i], m.Recipe.Name)
		}
	}
	return out
}

func (e env) setHousehold(t *testing.T, tz, weekStart string) {
	t.Helper()
	err := e.db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE households SET timezone = ?, week_start = ? WHERE id = ?`, tz, weekStart, e.hh.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e env) cooked(t *testing.T, recipe uuid.UUID, at time.Time) {
	t.Helper()
	err := e.sc.Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		now := store.T(at)
		_, err := q.Exec(ctx, `INSERT INTO cook_events (id, household_id, recipe_id, user_id, cooked_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, store.NewID(), e.sc.HouseholdID(), recipe, e.hh.Admin, now, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasKind(events []live.Event, k live.Kind) bool {
	return slices.ContainsFunc(events, func(e live.Event) bool { return e.Kind == k })
}

func TestCalendar(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		dal := e.recipe(t, "Dal")

		// UTC, weeks from Monday: today is Monday the 9th.
		if today, err := e.s.Today(ctx, e.sc); err != nil || today != date("2026-11-09") {
			t.Errorf("today %v, %v", today, err)
		}
		e.add(t, dal, "2026-11-15") // Sunday
		w, err := e.s.Week(ctx, e.sc, date("2026-11-12"))
		if err != nil || w.Start != date("2026-11-09") || len(w.Days) != 7 || w.Days[6].Date != date("2026-11-15") ||
			len(w.Days[6].Meals) != 1 {
			t.Fatalf("week from Monday: %+v, %v", w, err)
		}

		// Auckland is 13 hours ahead: it is already Tuesday the 10th there.
		// With weeks from Sunday, the 15th starts the next week.
		e.setHousehold(t, "Pacific/Auckland", "sun")
		if today, _ := e.s.Today(ctx, e.sc); today != date("2026-11-10") {
			t.Errorf("Auckland today %v", today)
		}
		w, err = e.s.Week(ctx, e.sc, date("2026-11-12"))
		if err != nil || w.Start != date("2026-11-08") || w.Today != date("2026-11-10") || len(w.Days[0].Meals) != 0 {
			t.Fatalf("week from Sunday: %+v, %v", w, err)
		}
		if w, _ := e.s.Week(ctx, e.sc, date("2026-11-15")); w.Start != date("2026-11-15") || len(w.Days[0].Meals) != 1 {
			t.Errorf("next week from Sunday: %+v", w)
		}

		// November 2026 starts on a Sunday and ends on a Monday.
		m, err := e.s.Month(ctx, e.sc, 2026, time.November)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Weeks) != 5 || m.Weeks[0][0].Date != date("2026-11-01") || m.Weeks[4][6].Date != date("2026-12-05") {
			t.Errorf("month from Sunday: %d weeks, %v … %v", len(m.Weeks), m.Weeks[0][0].Date, m.Weeks[len(m.Weeks)-1][6].Date)
		}
		e.setHousehold(t, "UTC", "mon")
		m, _ = e.s.Month(ctx, e.sc, 2026, time.November)
		if len(m.Weeks) != 6 || m.Weeks[0][0].Date != date("2026-10-26") || m.Weeks[5][6].Date != date("2026-12-06") ||
			len(m.Weeks[2][6].Meals) != 1 || m.Month != time.November {
			t.Errorf("month from Monday: %d weeks, %v … %v", len(m.Weeks), m.Weeks[0][0].Date, m.Weeks[len(m.Weeks)-1][6].Date)
		}
	})
}

func TestAdd(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		tofu := e.recipe(t, "Sticky tofu", "400 g firm tofu", "3 tbsp soy sauce", "1 thumb ginger")
		dal := e.recipe(t, "Dal", "250 g red lentils")

		// Soy sauce is on the list already; it stays as it is.
		if _, err := e.g.Add(ctx, e.sc, groceries.AddInput{ListID: e.list.ID, Name: "Soy sauce", Amount: groceries.ParseAmount("1 bottle", language.English),
			AddedBy: e.hh.Member, Lang: language.English}); err != nil {
			t.Fatal(err)
		}
		e.rec.take()

		a, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: tofu.ID, Date: date("2026-11-10"), Servings: 4, By: e.hh.Admin,
			Ingredients: []uuid.UUID{tofu.Ingredients[0].ID, tofu.Ingredients[1].ID, tofu.Ingredients[0].ID}, Lang: language.English})
		if err != nil {
			t.Fatal(err)
		}
		if a.Meal.Servings != 4 || a.Meal.Recipe.Name != "Sticky tofu" || a.Meal.Recipe.Servings != 2 || a.Meal.Position != 0 {
			t.Errorf("meal %+v", a.Meal)
		}
		if len(a.Shopping.Added) != 1 || a.Shopping.Added[0].ItemName != "Firm tofu" || a.Shopping.Added[0].Amount.Text != "800 g" ||
			a.Shopping.Added[0].RecipeID == nil || *a.Shopping.Added[0].RecipeID != tofu.ID ||
			!slices.Equal(a.Shopping.Present, []string{"Soy sauce"}) || a.Shopping.List.ID != e.list.ID {
			t.Errorf("shopping %+v", a.Shopping)
		}
		if ev := e.rec.take(); !hasKind(ev, live.PlanChanged) || !slices.Contains(ev, live.Event{Kind: live.ListChanged, ListID: e.list.ID}) {
			t.Errorf("events %+v", ev)
		}
		lv, err := e.g.ListView(ctx, e.sc, e.list.ID)
		if err != nil {
			t.Fatal(err)
		}
		var amounts []string
		for _, g := range lv.Groups {
			for _, en := range g.Entries {
				amounts = append(amounts, en.ItemName+" "+en.Amount.Text)
			}
		}
		slices.Sort(amounts)
		if !slices.Equal(amounts, []string{"Firm tofu 800 g", "Soy sauce 1 bottle"}) {
			t.Errorf("list %v", amounts)
		}

		// A second meal that day goes after the first, at the recipe's
		// servings; without picks the list is untouched.
		b, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: dal.ID, Date: date("2026-11-10"), By: e.hh.Member})
		if err != nil || b.Meal.Position != 1 || b.Meal.Servings != 2 {
			t.Errorf("second meal %+v, %v", b.Meal, err)
		}
		if ev := e.rec.take(); hasKind(ev, live.ListChanged) {
			t.Errorf("list event without picks: %+v", ev)
		}
		if got := e.dayNames(t, "2026-11-10")[1]; !slices.Equal(got, []string{"Sticky tofu", "Dal"}) {
			t.Errorf("Tuesday %v", got)
		}

		// Bad input changes nothing.
		var fe FieldErrors
		if _, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: dal.ID, By: e.hh.Admin}); !errors.As(err, &fe) || fe["date"] == "" {
			t.Errorf("no date: %v", err)
		}
		if _, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: dal.ID, Date: date("2026-11-11"), Servings: MaxServings + 1, By: e.hh.Admin}); !errors.As(err, &fe) {
			t.Errorf("too many servings: %v", err)
		}
		if _, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: uuid.New(), Date: date("2026-11-11"), By: e.hh.Admin}); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown recipe: %v", err)
		}
		// An ingredient of another recipe rolls the whole add back.
		if _, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: dal.ID, Date: date("2026-11-11"), By: e.hh.Admin,
			Ingredients: []uuid.UUID{tofu.Ingredients[2].ID}}); !errors.Is(err, ErrNotFound) {
			t.Errorf("other recipe's ingredient: %v", err)
		}
		if got := e.dayNames(t, "2026-11-11")[2]; len(got) != 0 {
			t.Errorf("rolled back add left %v", got)
		}
	})
}

// TestUnplan covers the Undo of planning: the meal and its untouched
// entries go in one transaction; entries changed since stay.
func TestUnplan(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		tofu := e.recipe(t, "Sticky tofu", "400 g firm tofu", "3 tbsp soy sauce", "1 thumb ginger")
		dal := e.recipe(t, "Dal", "250 g red lentils")
		ings := func(r recipes.Recipe) []uuid.UUID {
			ids := make([]uuid.UUID, len(r.Ingredients))
			for i, ing := range r.Ingredients {
				ids[i] = ing.ID
			}
			return ids
		}
		a, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: tofu.ID, Date: date("2026-11-10"), By: e.hh.Admin, Ingredients: ings(tofu), Lang: language.English})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: dal.ID, Date: date("2026-11-10"), By: e.hh.Admin, Ingredients: ings(dal), Lang: language.English})
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Shopping.Added) != 3 || len(d.Shopping.Added) != 1 {
			t.Fatalf("added %d and %d entries", len(a.Shopping.Added), len(d.Shopping.Added))
		}
		tofuEntry, soy, ginger, lentils := a.Shopping.Added[0].ID, a.Shopping.Added[1].ID, a.Shopping.Added[2].ID, d.Shopping.Added[0].ID

		// Later, someone adds more tofu (merged into the planned entry)
		// and checks off the soy sauce.
		e.clk.Add(time.Minute)
		more, err := e.g.Add(ctx, e.sc, groceries.AddInput{ListID: e.list.ID, Name: "Firm tofu",
			Amount: groceries.ParseAmount("200 g", language.English), AddedBy: e.hh.Member, Lang: language.English})
		if err != nil || !more.Merged || more.Entry.ID != tofuEntry {
			t.Fatalf("merge %+v, %v", more, err)
		}
		if _, err := e.g.Check(ctx, e.sc, soy); err != nil {
			t.Fatal(err)
		}
		e.rec.take()

		// A meal that is gone rolls back: the entries stay.
		if _, err := e.s.Unplan(ctx, e.sc, uuid.New(), []uuid.UUID{ginger}, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown meal: %v", err)
		}
		if _, err := e.g.Entry(ctx, e.sc, ginger); err != nil {
			t.Errorf("rolled back unplan removed ginger: %v", err)
		}

		// The dal's lentils and an unknown ID are not the tofu's: they are
		// left alone.
		m, err := e.s.Unplan(ctx, e.sc, a.Meal.ID, []uuid.UUID{tofuEntry, soy, ginger, lentils, uuid.New()}, nil)
		if err != nil || m.ID != a.Meal.ID {
			t.Fatalf("unplan %+v, %v", m, err)
		}
		if got := e.dayNames(t, "2026-11-10")[1]; !slices.Equal(got, []string{"Dal"}) {
			t.Errorf("Tuesday %v", got)
		}
		if _, err := e.g.Entry(ctx, e.sc, ginger); !errors.Is(err, groceries.ErrNotFound) {
			t.Errorf("untouched ginger stayed: %v", err)
		}
		if en, err := e.g.Entry(ctx, e.sc, tofuEntry); err != nil || en.Amount.Quantity == nil || *en.Amount.Quantity != 600 {
			t.Errorf("merged tofu %+v, %v", en, err)
		}
		if en, err := e.g.Entry(ctx, e.sc, soy); err != nil || !en.Checked() {
			t.Errorf("checked soy sauce %+v, %v", en, err)
		}
		if _, err := e.g.Entry(ctx, e.sc, lentils); err != nil {
			t.Errorf("dal's lentils: %v", err)
		}
		if ev := e.rec.take(); !hasKind(ev, live.PlanChanged) || !slices.Contains(ev, live.Event{Kind: live.ListChanged, ListID: e.list.ID}) {
			t.Errorf("events %+v", ev)
		}

		// A second Undo finds the meal gone.
		if _, err := e.s.Unplan(ctx, e.sc, a.Meal.ID, nil, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("second unplan: %v", err)
		}

		// Without entries taken back, no list event.
		if _, err := e.s.Unplan(ctx, e.sc, d.Meal.ID, nil, nil); err != nil {
			t.Fatal(err)
		}
		if ev := e.rec.take(); hasKind(ev, live.ListChanged) {
			t.Errorf("list event without entries: %+v", ev)
		}
	})
}

func TestMoveAndSwap(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		tofu, shak, dal, soup := e.recipe(t, "Tofu"), e.recipe(t, "Shakshuka"), e.recipe(t, "Dal"), e.recipe(t, "Soup")
		mTofu := e.add(t, tofu, "2026-11-09")
		mShak := e.add(t, shak, "2026-11-11")
		e.add(t, dal, "2026-11-11")
		mSoup := e.add(t, soup, "2026-11-09")
		e.rec.take()
		week := func() [][]string { return e.dayNames(t, "2026-11-09") }

		// Moving appends to the day.
		ch, err := e.s.Move(ctx, e.sc, mTofu.ID, date("2026-11-11"))
		if err != nil || ch.From != date("2026-11-09") || ch.Meal.Date != date("2026-11-11") || ch.With != nil || len(ch.Undo) != 1 {
			t.Fatalf("move %+v, %v", ch, err)
		}
		if w := week(); !slices.Equal(w[0], []string{"Soup"}) || !slices.Equal(w[2], []string{"Shakshuka", "Dal", "Tofu"}) {
			t.Errorf("after move %v", w)
		}
		if !hasKind(e.rec.take(), live.PlanChanged) {
			t.Error("move published nothing")
		}
		if err := e.s.Place(ctx, e.sc, ch.Undo); err != nil {
			t.Fatal(err)
		}
		if w := week(); !slices.Equal(w[0], []string{"Tofu", "Soup"}) || !slices.Equal(w[2], []string{"Shakshuka", "Dal"}) {
			t.Errorf("after undoing the move %v", w)
		}

		// Swapping trades places with the day's first meal only.
		ch, err = e.s.Swap(ctx, e.sc, mSoup.ID, date("2026-11-11"))
		if err != nil || ch.With == nil || ch.With.ID != mShak.ID || ch.With.Date != date("2026-11-09") || len(ch.Undo) != 2 {
			t.Fatalf("swap %+v, %v", ch, err)
		}
		if w := week(); !slices.Equal(w[0], []string{"Tofu", "Shakshuka"}) || !slices.Equal(w[2], []string{"Soup", "Dal"}) {
			t.Errorf("after swap %v", w)
		}
		if err := e.s.Place(ctx, e.sc, ch.Undo); err != nil {
			t.Fatal(err)
		}
		if w := week(); !slices.Equal(w[0], []string{"Tofu", "Soup"}) || !slices.Equal(w[2], []string{"Shakshuka", "Dal"}) {
			t.Errorf("after undoing the swap %v", w)
		}

		// Swapping onto a free day is a move; onto its own day nothing.
		if ch, err = e.s.Swap(ctx, e.sc, mTofu.ID, date("2026-11-14")); err != nil || ch.With != nil || ch.Meal.Date != date("2026-11-14") {
			t.Errorf("swap onto a free day %+v, %v", ch, err)
		}
		e.rec.take()
		if ch, err = e.s.Move(ctx, e.sc, mTofu.ID, date("2026-11-14")); err != nil || len(ch.Undo) != 0 {
			t.Errorf("move onto its own day %+v, %v", ch, err)
		}
		if ev := e.rec.take(); len(ev) != 0 {
			t.Errorf("no-op move published %+v", ev)
		}

		// Undo of a meal removed meanwhile changes nothing.
		ch, _ = e.s.Swap(ctx, e.sc, mTofu.ID, date("2026-11-11"))
		if _, err := e.s.Remove(ctx, e.sc, mShak.ID); err != nil {
			t.Fatal(err)
		}
		before := week()
		if err := e.s.Place(ctx, e.sc, ch.Undo); !errors.Is(err, ErrNotFound) {
			t.Errorf("undo with a removed meal: %v", err)
		}
		if after := week(); !slices.EqualFunc(before, after, slices.Equal) {
			t.Errorf("failed undo changed the week: %v → %v", before, after)
		}
		if _, err := e.s.Move(ctx, e.sc, mShak.ID, date("2026-11-12")); !errors.Is(err, ErrNotFound) {
			t.Errorf("move a removed meal: %v", err)
		}
	})
}

func TestChangeMeal(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		tofu, dal := e.recipe(t, "Tofu"), e.recipe(t, "Dal")
		m := e.add(t, tofu, "2026-11-10")

		if old, err := e.s.SetServings(ctx, e.sc, m.ID, 5); err != nil || old != 2 {
			t.Errorf("servings: %d, %v", old, err)
		}
		var fe FieldErrors
		if _, err := e.s.SetServings(ctx, e.sc, m.ID, 0); !errors.As(err, &fe) {
			t.Errorf("zero servings: %v", err)
		}
		old, err := e.s.Replace(ctx, e.sc, m.ID, dal.ID)
		if err != nil || old != tofu.ID {
			t.Errorf("replace: %v, %v", old, err)
		}
		got, err := e.s.Get(ctx, e.sc, m.ID)
		if err != nil || got.Recipe.ID != dal.ID || got.Servings != 5 || got.Date != date("2026-11-10") {
			t.Errorf("replaced meal %+v, %v", got, err)
		}
		if _, err := e.s.Replace(ctx, e.sc, m.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
			t.Errorf("replace with unknown recipe: %v", err)
		}

		// Remove and Restore, within the Undo window only.
		e.rec.take()
		if _, err := e.s.Remove(ctx, e.sc, m.ID); err != nil {
			t.Fatal(err)
		}
		if !hasKind(e.rec.take(), live.PlanChanged) {
			t.Error("remove published nothing")
		}
		if _, err := e.s.Get(ctx, e.sc, m.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("removed meal: %v", err)
		}
		if _, err := e.s.Remove(ctx, e.sc, m.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("remove twice: %v", err)
		}
		if err := e.s.Restore(ctx, e.sc, m.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Get(ctx, e.sc, m.ID); err != nil {
			t.Errorf("restored meal: %v", err)
		}
		if _, err := e.s.Remove(ctx, e.sc, m.ID); err != nil {
			t.Fatal(err)
		}
		e.clk.Add(UndoWindow + time.Minute)
		if err := e.s.Restore(ctx, e.sc, m.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("restore after the window: %v", err)
		}
	})
}

func TestDeletedRecipeHidesMeals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		tofu := e.recipe(t, "Tofu")
		m := e.add(t, tofu, "2026-11-12")
		if dates, err := e.s.PlannedFor(ctx, e.sc, tofu.ID); err != nil || !slices.Equal(dates, []store.Date{date("2026-11-12")}) {
			t.Errorf("planned for %v, %v", dates, err)
		}
		if err := e.r.Delete(ctx, e.sc, tofu.ID); err != nil {
			t.Fatal(err)
		}
		if got := e.dayNames(t, "2026-11-12")[3]; len(got) != 0 {
			t.Errorf("meal of a deleted recipe shows: %v", got)
		}
		if _, err := e.s.Get(ctx, e.sc, m.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("meal of a deleted recipe: %v", err)
		}
		if _, err := e.s.Add(ctx, e.sc, AddInput{RecipeID: tofu.ID, Date: date("2026-11-13"), By: e.hh.Admin}); !errors.Is(err, ErrNotFound) {
			t.Errorf("plan a deleted recipe: %v", err)
		}
		if err := e.r.Restore(ctx, e.sc, tofu.ID); err != nil {
			t.Fatal(err)
		}
		if got := e.dayNames(t, "2026-11-12")[3]; !slices.Equal(got, []string{"Tofu"}) {
			t.Errorf("meal back with its recipe: %v", got)
		}
		// Past days do not count for the delete confirmation.
		e.clk.Add(4 * 24 * time.Hour)
		if dates, err := e.s.PlannedFor(ctx, e.sc, tofu.ID); err != nil || len(dates) != 0 {
			t.Errorf("planned for after the day: %v, %v", dates, err)
		}
	})
}

func TestSuggestions(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		recent := e.recipe(t, "Recent")
		e.cooked(t, recent.ID, start.Add(-20*24*time.Hour))
		fiveWeeks := e.recipe(t, "Five weeks")
		e.cooked(t, fiveWeeks.ID, start.Add(-35*24*time.Hour))
		twoMonths := e.recipe(t, "Two months")
		e.cooked(t, twoMonths.ID, start.Add(-60*24*time.Hour))
		e.cooked(t, twoMonths.ID, start.Add(-90*24*time.Hour))
		e.clk.Add(time.Minute)
		never := e.recipe(t, "Never")
		e.clk.Add(time.Minute)
		newer := e.recipe(t, "Never, newer")
		planned := e.recipe(t, "Planned")
		e.cooked(t, planned.ID, start.Add(-100*24*time.Hour))
		e.add(t, planned, "2026-11-13")

		names := func(ss []Suggestion) []string {
			var out []string
			for _, s := range ss {
				out = append(out, s.Recipe.Name)
			}
			return out
		}
		got, err := e.s.Suggestions(ctx, e.sc, date("2026-11-09"), date("2026-11-16"), 10)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"Two months", "Five weeks", "Never", "Never, newer"}; !slices.Equal(names(got), want) {
			t.Errorf("suggestions %v, want %v", names(got), want)
		}
		if got[0].LastCookedAt == nil || !got[0].LastCookedAt.Equal(start.Add(-60*24*time.Hour)) || got[2].LastCookedAt != nil {
			t.Errorf("last cooked %v, %v", got[0].LastCookedAt, got[2].LastCookedAt)
		}
		// Planned in another week, it is suggested again; limit applies.
		got, _ = e.s.Suggestions(ctx, e.sc, date("2026-11-16"), date("2026-11-23"), 2)
		if want := []string{"Planned", "Two months"}; !slices.Equal(names(got), want) {
			t.Errorf("next week's suggestions %v, want %v", names(got), want)
		}

		// Tonight: the first suggestion not planned this week, until a
		// meal is planned for today.
		tn, err := e.s.Tonight(ctx, e.sc)
		if err != nil || tn.Meal != nil || tn.Suggestion == nil || tn.Suggestion.Recipe.ID != twoMonths.ID {
			t.Errorf("tonight %+v, %v", tn, err)
		}
		e.add(t, newer, "2026-11-09")
		e.add(t, never, "2026-11-09")
		if tn, err = e.s.Tonight(ctx, e.sc); err != nil || tn.Meal == nil || tn.Meal.Recipe.ID != newer.ID || tn.Suggestion != nil {
			t.Errorf("tonight with a meal %+v, %v", tn, err)
		}
	})
}

func TestCookEvents(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "home")
		tofu := e.recipe(t, "Tofu")
		ev, err := e.s.MarkCooked(ctx, e.sc, tofu.ID, e.hh.Member)
		if err != nil {
			t.Fatal(err)
		}
		events := e.rec.take()
		if !slices.Contains(events, live.Event{Kind: live.RecipeChanged, RecipeID: tofu.ID}) || !hasKind(events, live.PlanChanged) ||
			!hasKind(events, live.RecipesChanged) {
			t.Errorf("events %+v", events)
		}
		r, err := e.r.Get(ctx, e.sc, tofu.ID, e.hh.Admin)
		if err != nil || r.TimesCooked != 1 || r.LastCookedAt == nil || !r.LastCookedAt.Equal(start) {
			t.Errorf("after cooking: %d times, last %v, %v", r.TimesCooked, r.LastCookedAt, err)
		}
		if err := e.s.UndoCooked(ctx, e.sc, uuid.New(), ev); !errors.Is(err, ErrNotFound) {
			t.Errorf("undo with another recipe: %v", err)
		}
		if err := e.s.UndoCooked(ctx, e.sc, tofu.ID, ev); err != nil {
			t.Fatal(err)
		}
		if r, _ := e.r.Get(ctx, e.sc, tofu.ID, e.hh.Admin); r.TimesCooked != 0 {
			t.Errorf("after undo: %d times", r.TimesCooked)
		}
		if err := e.s.UndoCooked(ctx, e.sc, tofu.ID, ev); !errors.Is(err, ErrNotFound) {
			t.Errorf("undo twice: %v", err)
		}
		ev, _ = e.s.MarkCooked(ctx, e.sc, tofu.ID, e.hh.Member)
		e.clk.Add(UndoWindow + time.Minute)
		if err := e.s.UndoCooked(ctx, e.sc, tofu.ID, ev); !errors.Is(err, ErrNotFound) {
			t.Errorf("undo after the window: %v", err)
		}
		if _, err := e.s.MarkCooked(ctx, e.sc, uuid.New(), e.hh.Member); !errors.Is(err, ErrNotFound) {
			t.Errorf("cook an unknown recipe: %v", err)
		}
	})
}

// TestAddAlso plans linked recipes with a meal and takes them back with
// its Undo.
func TestAddAlso(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "also")
		curry := e.recipe(t, "Curry")
		rice := e.recipe(t, "Rice")
		naan := e.recipe(t, "Naan")
		gone := e.recipe(t, "Raita")
		if err := e.r.Delete(ctx, e.sc, gone.ID); err != nil {
			t.Fatal(err)
		}
		e.add(t, naan, "2026-11-10")

		a, err := e.s.Add(ctx, e.sc, AddInput{
			RecipeID: curry.ID, Date: date("2026-11-10"), Servings: 4, By: e.hh.Admin, Lang: language.English,
			Also: []uuid.UUID{rice.ID, rice.ID, gone.ID, curry.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Also) != 1 || a.Also[0].Recipe.ID != rice.ID || a.Also[0].Servings != 4 {
			t.Fatalf("also %+v", a.Also)
		}
		if got := e.dayNames(t, "2026-11-10")[1]; !slices.Equal(got, []string{"Naan", "Curry", "Rice"}) {
			t.Errorf("Tuesday %v", got)
		}

		if _, err := e.s.Unplan(ctx, e.sc, a.Meal.ID, nil, []uuid.UUID{a.Also[0].ID, uuid.New()}); err != nil {
			t.Fatal(err)
		}
		if got := e.dayNames(t, "2026-11-10")[1]; !slices.Equal(got, []string{"Naan"}) {
			t.Errorf("after undo %v", got)
		}
	})
}
