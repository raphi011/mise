package discover_test

import (
	"context"
	"slices"
	"testing"

	"github.com/raphi011/mise/internal/discover"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestSiteSettings: sites in the household's language are searched by
// default; an admin's choice otherwise is kept, a choice back to the
// default follows the language again.
func TestSiteSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		hh := storetest.NewHousehold(t, db, "home") // default language "en"
		sc := db.ScopeFor(hh.ID)
		s := discover.New(&fakeFetcher{}, discover.Sites(), nil)

		english := []string{"bbcgoodfood.com", "recipetineats.com", "smittenkitchen.com", "minimalistbaker.com"}
		if got, err := s.Enabled(ctx, sc); err != nil || !slices.Equal(got, english) {
			t.Fatalf("default: %q, %v", got, err)
		}
		settings, err := s.Settings(ctx, sc)
		if err != nil || len(settings) != len(discover.Sites()) {
			t.Fatalf("settings: %d, %v", len(settings), err)
		}

		// Turn off a default site and turn on two others.
		on := []string{"chefkoch.de", "recipetineats.com", "smittenkitchen.com", "minimalistbaker.com", "marmiton.org", "unknown.example"}
		if err := s.SaveSettings(ctx, sc, on); err != nil {
			t.Fatal(err)
		}
		want := []string{"chefkoch.de", "recipetineats.com", "smittenkitchen.com", "minimalistbaker.com", "marmiton.org"}
		if got, _ := s.Enabled(ctx, sc); !slices.Equal(got, want) {
			t.Errorf("after save: %q, want %q", got, want)
		}
		var stored []string
		if err := sc.Read().Select(ctx, &stored, `SELECT site FROM household_recipe_sites WHERE household_id = ? ORDER BY site`, hh.ID); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(stored, []string{"bbcgoodfood.com", "chefkoch.de", "marmiton.org"}) {
			t.Errorf("stored %q: only choices off the default are kept", stored)
		}

		// When the household's language changes, sites left at the default
		// follow it; the choices stay.
		if err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE households SET default_language = 'de' WHERE id = ?`, hh.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		want = []string{"chefkoch.de", "ichkoche.at", "gutekueche.de", "eatsmarter.de", "emmikochteinfach.de", "eat-this.org", "marmiton.org"}
		if got, _ := s.Enabled(ctx, sc); !slices.Equal(got, want) {
			t.Errorf("in German: %q, want %q", got, want)
		}

		// Saving everything off is kept too.
		if err := s.SaveSettings(ctx, sc, nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Enabled(ctx, sc); len(got) != 0 {
			t.Errorf("all off: %q", got)
		}
	})
}
