package recipes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestSlugify(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"Sticky tofu", "sticky-tofu"},
		{"  Spaghetti   aglio e olio! ", "spaghetti-aglio-e-olio"},
		{"Grießnockerlsuppe", "griessnockerlsuppe"},
		{"Käsespätzle mit Röstzwiebeln", "kaesespaetzle-mit-roestzwiebeln"},
		{"ÜBERBACKENE Äpfel", "ueberbackene-aepfel"},
		{"Crème brûlée", "creme-brulee"},
		{"Smørrebrød", "smorrebrod"},
		{"Pierogi z Łodzi", "pierogi-z-lodzi"},
		{"Mac & cheese (2024)", "mac-cheese-2024"},
		{"Dal – red lentils", "dal-red-lentils"},
		{"麻婆豆腐", "recipe"},
		{"🍕🍕", "recipe"},
		{"Pizza 🍕 night", "pizza-night"},
		{"", "recipe"},
		{strings.Repeat("very long name ", 10), "very-long-name-very-long-name-very-long-name-very-long-name"},
		{strings.Repeat("x", 70), strings.Repeat("x", 60)},
	} {
		if got := Slugify(tt.name); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSlugs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "Home")
		slug := func(r Recipe) string {
			t.Helper()
			got, err := e.s.Get(ctx, e.sc, r.ID, e.hh.Admin)
			if err != nil {
				t.Fatal(err)
			}
			return got.Slug
		}
		rename := func(r Recipe, name string) {
			t.Helper()
			if err := e.s.Update(ctx, e.sc, r.ID, e.hh.Admin, Input{Name: name, Servings: 2}); err != nil {
				t.Fatal(err)
			}
		}
		resolve := func(handle string, want Recipe, wantSlug string) {
			t.Helper()
			id, got, err := e.s.Resolve(ctx, e.sc, handle)
			if err != nil || id != want.ID || got != wantSlug {
				t.Errorf("Resolve(%q) = %v %q %v, want %v %q", handle, id, got, err, want.ID, wantSlug)
			}
		}

		// Same names get the lowest free suffix; reserved paths and names
		// that read as an ID always get one.
		curry := e.create(t, Input{Name: "Green curry"})
		curry2 := e.create(t, Input{Name: "green CURRY"})
		curry3 := e.create(t, Input{Name: "Green curry!"})
		for i, r := range []Recipe{curry, curry2, curry3} {
			if want := []string{"green-curry", "green-curry-2", "green-curry-3"}[i]; slug(r) != want {
				t.Errorf("%q: slug %q, want %q", r.Name, slug(r), want)
			}
		}
		if got := e.create(t, Input{Name: "New"}).Slug; got != "new-2" {
			t.Errorf("reserved name: slug %q", got)
		}
		if got := e.create(t, Input{Name: "0193a1b2c3d4e5f60718293a4b5c6d7e"}).Slug; got != "0193a1b2c3d4e5f60718293a4b5c6d7e-2" {
			t.Errorf("ID-like name: slug %q", got)
		}

		// Resolve takes the slug in any case, and the ID.
		resolve("green-curry-2", curry2, "green-curry-2")
		resolve("Green-Curry", curry, "green-curry")
		resolve(curry3.ID.String(), curry3, "green-curry-3")
		for _, handle := range []string{"", "green", "green-curry-4", strings.Repeat("a", MaxHandleLen+1)} {
			if _, _, err := e.s.Resolve(ctx, e.sc, handle); !errors.Is(err, ErrNotFound) {
				t.Errorf("Resolve(%q): %v, want ErrNotFound", handle, err)
			}
		}

		// A rename moves the slug and keeps the old one as a redirect; an
		// edit that keeps the name keeps the slug, even when a lower suffix
		// came free meanwhile.
		rename(curry, "Red curry")
		resolve("green-curry", curry, "red-curry")
		rename(curry3, "Green curry!")
		if got := slug(curry3); got != "green-curry-3" {
			t.Errorf("unchanged name moved the slug to %q", got)
		}

		// A new recipe takes a freed slug, and the old link now opens it.
		fresh := e.create(t, Input{Name: "Green curry"})
		if fresh.Slug != "green-curry" {
			t.Errorf("freed slug not reused: %q", fresh.Slug)
		}
		resolve("green-curry", fresh, "green-curry")

		// Renaming back takes the old slug again; no history entry points
		// at a recipe's own current slug, so nothing loops.
		rename(curry, "Yellow curry")
		rename(curry, "Red curry")
		resolve("red-curry", curry, "red-curry")
		resolve("yellow-curry", curry, "red-curry")
		var loops int
		if err := e.sc.Read().QueryRow(ctx, `SELECT count(*) FROM recipe_slugs h JOIN recipes r
			ON r.household_id = h.household_id AND r.slug = h.slug WHERE h.household_id = ?`, e.sc.HouseholdID()).Scan(&loops); err != nil || loops != 0 {
			t.Errorf("history entries equal to current slugs: %d, %v", loops, err)
		}

		// A binned recipe keeps its slug until it is swept.
		if err := e.s.Delete(ctx, e.sc, curry2.ID); err != nil {
			t.Fatal(err)
		}
		if got := e.create(t, Input{Name: "Green curry"}).Slug; got != "green-curry-4" {
			t.Errorf("slug of a binned recipe reused: %q", got)
		}
		resolve("green-curry-2", curry2, "green-curry-2")
	})
}

func TestSlugMissing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		e := newEnv(t, db, "Home")
		a := e.create(t, Input{Name: "Dal"})
		b := e.create(t, Input{Name: "Dal"})
		c := e.create(t, Input{Name: "Soup"})
		// Recipes from before slugs have their ID as the slug.
		if err := e.sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE recipes SET slug = `+idText(db)+` WHERE household_id = ? AND id <> ?`, e.sc.HouseholdID(), c.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		resolve := func(handle string) string {
			t.Helper()
			_, slug, err := e.s.Resolve(ctx, e.sc, handle)
			if err != nil {
				t.Fatal(err)
			}
			return slug
		}
		if got := resolve(a.ID.String()); got != a.ID.String() {
			t.Fatalf("placeholder slug %q", got)
		}
		if n, err := e.s.SlugMissing(ctx, e.sc); err != nil || n != 2 {
			t.Fatalf("slug missing: %d, %v", n, err)
		}
		if got := resolve(a.ID.String()); got != "dal" {
			t.Errorf("first dal: %q", got)
		}
		if got := resolve(b.ID.String()); got != "dal-2" {
			t.Errorf("second dal: %q", got)
		}
		if got := resolve(c.ID.String()); got != "soup" {
			t.Errorf("soup changed: %q", got)
		}
		if n, err := e.s.SlugMissing(ctx, e.sc); err != nil || n != 0 {
			t.Errorf("second run: %d, %v", n, err)
		}
		// The placeholder is no address anyone used, so it leaves no history.
		var history int
		if err := e.sc.Read().QueryRow(ctx, `SELECT count(*) FROM recipe_slugs WHERE household_id = ?`, e.sc.HouseholdID()).Scan(&history); err != nil || history != 0 {
			t.Errorf("history %d, %v", history, err)
		}
	})
}

// idText is the SQL that reads recipes.id as text.
func idText(db *store.DB) string {
	if db.Dialect() == store.Postgres {
		return "id::text"
	}
	return "id"
}
