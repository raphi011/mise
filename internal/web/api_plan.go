package web

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/plan"
	"github.com/raphi011/mise/internal/store"
)

type mealJSON struct {
	ID       string   `json:"id"`
	Date     string   `json:"date"`
	Recipe   linkJSON `json:"recipe"`
	Servings int      `json:"servings"`
}

func mealToJSON(m plan.Meal) mealJSON {
	return mealJSON{ID: m.ID.String(), Date: m.Date.String(), Recipe: linkJSON{ID: m.Recipe.ID.String(), Name: m.Recipe.Name}, Servings: m.Servings}
}

// apiGetPlan answers with the week that contains ?date=YYYY-MM-DD (default
// today), from the household's first day of the week.
func (s *Server) apiGetPlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	date := dateParam(r.URL.Query().Get("date"))
	if date.IsZero() {
		if raw := r.URL.Query().Get("date"); raw != "" {
			apiFail(w, http.StatusBadRequest, "invalid_date", "date must look like 2026-11-09.", nil)
			return
		}
		if date, err = s.plan.Today(ctx, sc); err != nil {
			s.apiErr(w, r, err)
			return
		}
	}
	week, err := s.plan.Week(ctx, sc, date)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	type dayJSON struct {
		Date  string     `json:"date"`
		Meals []mealJSON `json:"meals"`
	}
	out := struct {
		Start string    `json:"start"`
		Today string    `json:"today"`
		Days  []dayJSON `json:"days"`
	}{Start: week.Start.String(), Today: week.Today.String(), Days: []dayJSON{}}
	for _, d := range week.Days {
		day := dayJSON{Date: d.Date.String(), Meals: []mealJSON{}}
		for _, m := range d.Meals {
			day.Meals = append(day.Meals, mealToJSON(m))
		}
		out.Days = append(out.Days, day)
	}
	writeJSON(w, http.StatusOK, out)
}

// apiAddMeal plans a recipe for a day. With add_to_list, the recipe's
// required ingredients go on the default shopping list, scaled to the
// servings.
func (s *Server) apiAddMeal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RecipeID  uuid.UUID `json:"recipe_id"`
		Date      string    `json:"date"`
		Servings  int       `json:"servings"`
		AddToList bool      `json:"add_to_list"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	id := identity(r)
	date, err := store.ParseDate(in.Date)
	if err != nil {
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "Some fields need another look.", map[string]string{"date": "Use a date like 2026-11-09."})
		return
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	meal := plan.MealInput{RecipeID: in.RecipeID, Servings: in.Servings}
	if in.AddToList {
		rec, err := s.recipes.Get(ctx, sc, in.RecipeID, id.UserID)
		if err != nil {
			s.apiErr(w, r, err)
			return
		}
		for _, ing := range rec.Ingredients {
			if !ing.Optional {
				meal.Ingredients = append(meal.Ingredients, ing.ID)
			}
		}
	}
	added, err := s.plan.Add(ctx, sc, plan.AddInput{Date: date, Meals: []plan.MealInput{meal}, By: id.UserID, Lang: i18n.Locale(ctx)})
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, mealToJSON(added.Meals[0]))
}

func (s *Server) apiRemoveMeal(w http.ResponseWriter, r *http.Request) {
	mealID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if _, err := s.plan.Remove(r.Context(), sc, mealID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
