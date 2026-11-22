package web

import (
	"context"
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

type dayJSON struct {
	Date  string     `json:"date"`
	Meals []mealJSON `json:"meals"`
}

type weekJSON struct {
	Start string    `json:"start"`
	Today string    `json:"today"`
	Days  []dayJSON `json:"days"`
}

// errInvalidDate rejects a date that is not YYYY-MM-DD.
var errInvalidDate = &apiProblem{status: http.StatusBadRequest, code: "invalid_date", message: "date must look like 2026-11-09."}

// doGetPlan returns the week that contains date (YYYY-MM-DD, default
// today), from the household's first day of the week.
func (s *Server) doGetPlan(ctx context.Context, date string) (weekJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return weekJSON{}, err
	}
	var day store.Date
	if date != "" {
		if day, err = store.ParseDate(date); err != nil {
			return weekJSON{}, errInvalidDate
		}
	} else if day, err = s.plan.Today(ctx, sc); err != nil {
		return weekJSON{}, err
	}
	week, err := s.plan.Week(ctx, sc, day)
	if err != nil {
		return weekJSON{}, err
	}
	out := weekJSON{Start: week.Start.String(), Today: week.Today.String(), Days: []dayJSON{}}
	for _, d := range week.Days {
		day := dayJSON{Date: d.Date.String(), Meals: []mealJSON{}}
		for _, m := range d.Meals {
			day.Meals = append(day.Meals, mealToJSON(m))
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

// addMealIn plans a recipe for a day.
type addMealIn struct {
	RecipeID  uuid.UUID `json:"recipe_id"`
	Date      string    `json:"date"`
	Servings  int       `json:"servings"`
	AddToList bool      `json:"add_to_list"`
}

// doAddMeal plans a recipe for a day. With AddToList, the recipe's
// required ingredients go on the default shopping list, scaled to the
// servings.
func (s *Server) doAddMeal(ctx context.Context, in addMealIn) (mealJSON, error) {
	id := ctxIdentity(ctx)
	date, err := store.ParseDate(in.Date)
	if err != nil {
		return mealJSON{}, &apiProblem{status: http.StatusUnprocessableEntity, code: "invalid", message: "Some fields need another look.",
			fields: map[string]string{"date": "Use a date like 2026-11-09."}}
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return mealJSON{}, err
	}
	meal := plan.MealInput{RecipeID: in.RecipeID, Servings: in.Servings}
	if in.AddToList {
		rec, err := s.recipes.Get(ctx, sc, in.RecipeID, id.UserID)
		if err != nil {
			return mealJSON{}, err
		}
		for _, ing := range rec.Ingredients {
			if !ing.Optional {
				meal.Ingredients = append(meal.Ingredients, ing.ID)
			}
		}
	}
	added, err := s.plan.Add(ctx, sc, plan.AddInput{Date: date, Meals: []plan.MealInput{meal}, By: id.UserID, Lang: i18n.Locale(ctx)})
	if err != nil {
		return mealJSON{}, err
	}
	return mealToJSON(added.Meals[0]), nil
}

func (s *Server) doRemoveMeal(ctx context.Context, mealID uuid.UUID) error {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return err
	}
	_, err = s.plan.Remove(ctx, sc, mealID)
	return err
}

func (s *Server) apiGetPlan(w http.ResponseWriter, r *http.Request) {
	out, err := s.doGetPlan(r.Context(), r.URL.Query().Get("date"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiAddMeal(w http.ResponseWriter, r *http.Request) {
	var in addMealIn
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := s.doAddMeal(r.Context(), in)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) apiRemoveMeal(w http.ResponseWriter, r *http.Request) {
	mealID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if err := s.doRemoveMeal(r.Context(), mealID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
