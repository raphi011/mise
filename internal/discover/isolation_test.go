package discover_test

import (
	"slices"
	"testing"

	"github.com/raphi011/mise/internal/discover"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdIsolation: one household's site choices do not reach
// another's.
func TestHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		a, b := db.ScopeFor(storetest.NewHousehold(t, db, "a").ID), db.ScopeFor(storetest.NewHousehold(t, db, "b").ID)
		s := discover.New(&fakeFetcher{}, discover.Sites(), nil)
		before, err := s.Enabled(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSettings(ctx, a, []string{"marmiton.org"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Enabled(ctx, a); !slices.Equal(got, []string{"marmiton.org"}) {
			t.Errorf("a: %q", got)
		}
		if got, _ := s.Enabled(ctx, b); !slices.Equal(got, before) {
			t.Errorf("b changed by a: %q, was %q", got, before)
		}
		settings, _ := s.Settings(ctx, b)
		for _, st := range settings {
			if st.Site.Name() == "marmiton.org" && st.Enabled {
				t.Error("b sees a's choice")
			}
		}
	})
}
