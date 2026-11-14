package auth

import (
	"errors"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func TestUpdateHousehold(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		valid := HouseholdSettings{Name: "  Home  ", Timezone: "Europe/Vienna", WeekStart: "sun", Language: "de"}

		if err := s.UpdateHousehold(ctx, f.sc, f.member, valid); !errors.Is(err, ErrForbidden) {
			t.Errorf("member: %v, want ErrForbidden", err)
		}
		var fe FieldErrors
		err := s.UpdateHousehold(ctx, f.sc, f.admin, HouseholdSettings{Timezone: "Local", WeekStart: "tue", Language: "fr"})
		if !errors.As(err, &fe) || fe["household"] != "form.error.required" || fe["timezone"] != "form.error.timezone" ||
			fe["week_start"] != "form.error.choice" || fe["language"] != "form.error.choice" {
			t.Errorf("invalid: %v", err)
		}
		if h, _ := s.Household(ctx, f.sc); h.Name != "home" || h.Language != "en" {
			t.Errorf("changed by invalid input: %+v", h)
		}

		if err := s.UpdateHousehold(ctx, f.sc, f.admin, valid); err != nil {
			t.Fatal(err)
		}
		want := HouseholdSettings{Name: "Home", Timezone: "Europe/Vienna", WeekStart: "sun", Language: "de"}
		if h, err := s.Household(ctx, f.sc); err != nil || h != want {
			t.Errorf("household %+v, %v", h, err)
		}
		// Sessions see the change on their next request.
		if id, _ := signInAs(t, s, "home-admin"); id.HouseholdName != "Home" || id.HouseholdLanguage != "de" ||
			id.Timezone != "Europe/Vienna" || id.WeekStart != "sun" {
			t.Errorf("identity %+v", id)
		}
	})
}
