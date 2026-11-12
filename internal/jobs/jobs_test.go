package jobs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestSweep(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		g := groceries.New(nil, clock)
		hh := storetest.NewHousehold(t, db, "home")
		sc := db.ScopeFor(hh.ID)

		list, err := g.Start(ctx, sc, groceries.Defaults{ListName: "List"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := g.CreateList(ctx, sc, "Other")
		if err != nil {
			t.Fatal(err)
		}
		addEntry := func(name string, l groceries.List) groceries.Entry {
			a, err := g.Add(ctx, sc, groceries.AddInput{ListID: l.ID, Name: name, AddedBy: hh.Admin, Lang: language.English})
			if err != nil {
				t.Fatal(err)
			}
			return a.Entry
		}
		keep := addEntry("keep", list)
		removed := addEntry("removed", list)
		addEntry("on deleted list", other)
		if _, err := g.Remove(ctx, sc, removed.ID); err != nil {
			t.Fatal(err)
		}
		if err := g.DeleteList(ctx, sc, other.ID); err != nil {
			t.Fatal(err)
		}

		s := NewSweeper(db, slog.New(slog.DiscardHandler), func() time.Time { return now }, 0)
		// Within the Undo window nothing goes.
		now = now.Add(groceries.UndoWindow - time.Minute)
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{}) {
			t.Fatalf("early sweep: %+v, %v", got, err)
		}
		if _, err := g.Restore(ctx, sc, removed.ID); err != nil {
			t.Fatalf("restore within window: %v", err)
		}
		if _, err := g.Remove(ctx, sc, removed.ID); err != nil {
			t.Fatal(err)
		}

		now = now.Add(groceries.UndoWindow + time.Minute)
		got, err := s.Sweep(ctx)
		if err != nil || got != (Swept{Entries: 1, Lists: 1}) {
			t.Fatalf("sweep: %+v, %v", got, err)
		}
		if _, err := g.Restore(ctx, sc, removed.ID); !errors.Is(err, groceries.ErrNotFound) {
			t.Errorf("swept entry restored: %v", err)
		}
		if err := g.RestoreList(ctx, sc, other.ID); !errors.Is(err, groceries.ErrNotFound) {
			t.Errorf("swept list restored: %v", err)
		}
		if _, err := g.Entry(ctx, sc, keep.ID); err != nil {
			t.Errorf("live entry swept: %v", err)
		}
		var n int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM list_entries`).Scan(&n); err != nil || n != 1 {
			t.Errorf("entries left: %d, %v", n, err)
		}
	})
}

func TestSeedGroceries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		g := groceries.New(nil, nil)
		fresh := storetest.NewHousehold(t, db, "fresh")
		started := storetest.NewHousehold(t, db, "started")
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE households SET default_language = 'de' WHERE id = ?`, fresh.ID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Start(ctx, db.ScopeFor(started.ID), groceries.Defaults{ListName: "Mine"}); err != nil {
			t.Fatal(err)
		}
		defaults := func(lang string) groceries.Defaults {
			return groceries.Defaults{ListName: "List " + lang, Categories: []groceries.NewCategory{{Name: "Obst", Color: "herb"}}}
		}
		for range 2 { // idempotent
			if err := SeedGroceries(ctx, db, g, defaults); err != nil {
				t.Fatal(err)
			}
		}
		lists, err := g.Lists(ctx, db.ScopeFor(fresh.ID))
		if err != nil || len(lists) != 1 || lists[0].Name != "List de" {
			t.Errorf("fresh household lists: %+v, %v", lists, err)
		}
		lists, err = g.Lists(ctx, db.ScopeFor(started.ID))
		if err != nil || len(lists) != 1 || lists[0].Name != "Mine" {
			t.Errorf("started household changed: %+v, %v", lists, err)
		}
	})
}

func TestSweepRecipes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		g := groceries.New(nil, clock)
		r := recipes.New(nil, g, clock)
		hh := storetest.NewHousehold(t, db, "home")
		sc := db.ScopeFor(hh.ID)
		if _, err := g.Start(ctx, sc, groceries.Defaults{ListName: "List"}); err != nil {
			t.Fatal(err)
		}
		in := recipes.Input{Name: "Dal", Servings: 2, Ingredients: []recipes.IngredientInput{
			recipes.ParseIngredient("250 g red lentils", language.English),
		}}
		gone, err := r.Create(ctx, sc, hh.Admin, in)
		if err != nil {
			t.Fatal(err)
		}
		in.Name = "Dal again"
		kept, err := r.Create(ctx, sc, hh.Admin, in)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := r.Get(ctx, sc, gone, hh.Admin)
		if err != nil {
			t.Fatal(err)
		}
		toggled, err := r.ToggleIngredient(ctx, sc, hh.Admin, gone, rec.Ingredients[0].ID, 2, true, language.English)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Delete(ctx, sc, gone); err != nil {
			t.Fatal(err)
		}

		s := NewSweeper(db, slog.New(slog.DiscardHandler), clock, 0)
		now = now.Add(recipes.UndoWindow + time.Minute)
		got, err := s.Sweep(ctx)
		if err != nil || got != (Swept{Recipes: 1}) {
			t.Fatalf("sweep: %+v, %v", got, err)
		}
		if err := r.Restore(ctx, sc, gone); !errors.Is(err, recipes.ErrNotFound) {
			t.Errorf("swept recipe restored: %v", err)
		}
		if _, err := r.Get(ctx, sc, kept, hh.Admin); err != nil {
			t.Errorf("live recipe swept: %v", err)
		}
		e, err := g.Entry(ctx, sc, toggled.Added.ID)
		if err != nil {
			t.Fatalf("list entry of swept recipe: %v", err)
		}
		if e.RecipeID != nil {
			t.Errorf("entry still tagged with swept recipe %v", e.RecipeID)
		}
		var n int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM recipe_ingredients`).Scan(&n); err != nil || n != 1 {
			t.Errorf("ingredients left: %d, %v", n, err)
		}
	})
}

func TestSweepImages(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		g := groceries.New(nil, clock)
		r := recipes.New(nil, g, clock)
		hh := storetest.NewHousehold(t, db, "home")
		sc := db.ScopeFor(hh.ID)
		st := images.New(t.TempDir())

		save := func(w int) string {
			var buf bytes.Buffer
			if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, 10))); err != nil {
				t.Fatal(err)
			}
			h, err := st.Save(ctx, hh.ID, &buf)
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		used, binned, orphan := save(10), save(11), save(12)
		if _, err := r.Create(ctx, sc, hh.Admin, recipes.Input{Name: "A", Servings: 1, Photo: used}); err != nil {
			t.Fatal(err)
		}
		id, err := r.Create(ctx, sc, hh.Admin, recipes.Input{Name: "B", Servings: 1, Photo: binned})
		if err != nil {
			t.Fatal(err)
		}

		s := NewSweeper(db, slog.New(slog.DiscardHandler), clock, 0).WithImages(st)
		// Files were just written: within the grace period nothing goes.
		now = time.Now()
		if got, err := s.Sweep(ctx); err != nil || got.Images != 0 {
			t.Fatalf("early sweep %+v, %v", got, err)
		}
		// Image files carry the real time, so the deletion is placed after
		// the grace period: the binned recipe is still in its Undo window.
		now = now.Add(ImageGrace + time.Minute)
		if err := r.Delete(ctx, sc, id); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Sweep(ctx); err != nil || got.Images != 1 {
			t.Fatalf("sweep %+v, %v", got, err)
		}
		if _, err := st.Open(hh.ID, orphan, images.Thumb); !errors.Is(err, images.ErrNotFound) {
			t.Errorf("orphan kept: %v", err)
		}
		for _, h := range []string{used, binned} {
			f, err := st.Open(hh.ID, h, images.Full)
			if err != nil {
				t.Errorf("referenced image swept: %v", err)
				continue
			}
			_ = f.Close()
		}
	})
}

func TestSweepPlannedMeals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		r := recipes.New(nil, groceries.New(nil, clock), clock)
		hh := storetest.NewHousehold(t, db, "home")
		sc := db.ScopeFor(hh.ID)
		kept, err := r.Create(ctx, sc, hh.Admin, recipes.Input{Name: "Dal", Servings: 2})
		if err != nil {
			t.Fatal(err)
		}
		binned, err := r.Create(ctx, sc, hh.Admin, recipes.Input{Name: "Soup", Servings: 2})
		if err != nil {
			t.Fatal(err)
		}
		// Planned meals are written by internal/plan; insert them directly.
		plan := func(recipe uuid.UUID, deleted bool) uuid.UUID {
			id := store.NewID()
			var deletedAt *store.Time
			if deleted {
				at := store.T(now)
				deletedAt = &at
			}
			err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
				_, err := q.Exec(ctx, `INSERT INTO planned_meals (id, household_id, date, recipe_id, servings, position,
						created_by, created_at, updated_at, deleted_at)
					VALUES (?, ?, ?, ?, 2, 0, ?, ?, ?, ?)`,
					id, hh.ID, store.DateOf(now), recipe, hh.Admin, store.T(now), store.T(now), deletedAt)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		live := plan(kept, false)
		plan(kept, true)
		plan(binned, false) // goes with its recipe
		if err := r.Delete(ctx, sc, binned); err != nil {
			t.Fatal(err)
		}

		s := NewSweeper(db, slog.New(slog.DiscardHandler), clock, 0)
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{}) {
			t.Fatalf("early sweep: %+v, %v", got, err)
		}
		now = now.Add(recipes.UndoWindow + time.Minute)
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{Meals: 1, Recipes: 1}) {
			t.Fatalf("sweep: %+v, %v", got, err)
		}
		var ids []uuid.UUID
		if err := db.System().Read().Select(ctx, &ids, `SELECT id FROM planned_meals`); err != nil || len(ids) != 1 || ids[0] != live {
			t.Errorf("planned meals left: %v, %v (want only %v)", ids, err, live)
		}
	})
}

func TestSweepResets(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		hh := storetest.NewHousehold(t, db, "home")
		insert := func(expires time.Time, used *time.Time) {
			t.Helper()
			var usedAt any
			if used != nil {
				usedAt = store.T(*used)
			}
			err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
				_, err := q.Exec(ctx, `INSERT INTO password_resets (id, token_hash, user_id, expires_at, used_at, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?)`, store.NewID(), uuid.NewString(), hh.Admin, store.T(expires), usedAt, store.T(now), store.T(now))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		insert(now.Add(time.Hour), nil)    // valid: stays
		insert(now.Add(-time.Minute), nil) // expired
		insert(now.Add(time.Hour), &now)   // used
		s := NewSweeper(db, slog.New(slog.DiscardHandler), func() time.Time { return now }, 0)
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{Resets: 2}) {
			t.Fatalf("sweep: %+v, %v", got, err)
		}
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{}) {
			t.Errorf("second sweep: %+v, %v", got, err)
		}
	})
}

func TestSweepImports(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
		hh := storetest.NewHousehold(t, db, "home")
		insert := func(status string, finished *time.Time) uuid.UUID {
			t.Helper()
			id := store.NewID()
			var fin any
			if finished != nil {
				fin = store.T(*finished)
			}
			err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
				_, err := q.Exec(ctx, `INSERT INTO import_jobs (id, household_id, user_id, source_url, status, steps, finished_at, created_at, updated_at)
					VALUES (?, ?, ?, 'https://example.com/r', ?, '[]', ?, ?, ?)`, id, hh.ID, hh.Admin, status, fin, store.T(now), store.T(now))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		old := now.Add(-ai.ReadyRetention - time.Minute)
		recent := now.Add(-ai.ReadyRetention + time.Minute)
		insert("ready", &old)
		insert("failed", &old)
		insert("canceled", &recent)
		keep := []uuid.UUID{insert("ready", &recent), insert("failed", &recent), insert("running", nil), insert("queued", nil)}
		s := NewSweeper(db, slog.New(slog.DiscardHandler), func() time.Time { return now }, 0)
		if got, err := s.Sweep(ctx); err != nil || got != (Swept{Imports: 3}) {
			t.Fatalf("sweep: %+v, %v", got, err)
		}
		var ids []uuid.UUID
		if err := db.System().Read().Select(ctx, &ids, `SELECT id FROM import_jobs`); err != nil || len(ids) != len(keep) {
			t.Errorf("imports left: %v, %v (want %v)", ids, err, keep)
		}
	})
}

func TestSplitIngredientAmounts(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		g := groceries.New(nil, nil)
		r := recipes.New(nil, g, nil)
		hh := storetest.NewHousehold(t, db, "home")
		sc := db.ScopeFor(hh.ID)
		if _, err := g.Start(ctx, sc, groceries.Defaults{ListName: "List"}); err != nil {
			t.Fatal(err)
		}
		// Stored the way the editor saved them before notes existed.
		id, err := r.Create(ctx, sc, hh.Admin, recipes.Input{Name: "Laibchen", Servings: 4, Ingredients: []recipes.IngredientInput{
			{Name: "Faschiertes", Amount: groceries.Amount{Text: "600 g gemischt"}},
			{Name: "Majoran", Amount: groceries.Amount{Text: "2 TL getrocknet"}},
			{Name: "Salz", Amount: groceries.Amount{Text: "nach Geschmack"}},
			{Name: "Milch", Amount: groceries.ParseAmount("150 ml", language.German)},
		}})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []int{2, 0} { // the second run changes nothing
			n, err := SplitIngredientAmounts(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := r.Get(ctx, sc, id, hh.Admin)
			if err != nil {
				t.Fatal(err)
			}
			got := rec.Ingredients
			if q := got[0].Amount.Quantity; q == nil || *q != 600 || got[0].Amount.Unit != "g" || got[0].Amount.Text != "600 g" || got[0].Note != "gemischt" {
				t.Errorf("Faschiertes: %+v %q", got[0].Amount, got[0].Note)
			}
			if q := got[1].Amount.Quantity; q == nil || *q != 2 || got[1].Amount.Unit != "tsp" || got[1].Note != "getrocknet" {
				t.Errorf("Majoran: %+v %q", got[1].Amount, got[1].Note)
			}
			if got[2].Amount.Quantity != nil || got[2].Amount.Text != "nach Geschmack" || got[2].Note != "" {
				t.Errorf("Salz: %+v %q", got[2].Amount, got[2].Note)
			}
			if n != want {
				t.Errorf("changed %d rows, want %d", n, want)
			}
		}
	})
}
