package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/catalog"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/nutrition"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// nutritionLines turns a recipe's ingredients into the lines the estimate
// is made of.
func nutritionLines(rec recipes.Recipe) []nutrition.Line {
	lines := make([]nutrition.Line, len(rec.Ingredients))
	for i, ing := range rec.Ingredients {
		lines[i] = nutrition.Line{ItemID: ing.ItemID, Name: ing.ItemName, Amount: ing.Amount, Optional: ing.Optional}
	}
	return lines
}

func pluralKey(ctx context.Context, n int, one, other i18n.Key) string {
	if n == 1 {
		return i18n.T(ctx, one)
	}
	return i18n.T(ctx, other, n)
}

func grams(ctx context.Context, v float64) string {
	return groceries.FormatQuantity(nutrition.RoundGrams(v), i18n.Locale(ctx)) + " g"
}

// nutritionBlock builds the block of a recipe page from the estimate, or
// nil when there is nothing to say: no ingredients with an amount or, on the
// public page, too little data to show numbers.
//
// amounts are the lines' amounts as the viewer reads them, in the unit
// system they chose; the missing list shows them.
func nutritionBlock(ctx context.Context, recipeID uuid.UUID, res nutrition.Result, amounts []string, public bool) *ui.NutritionView {
	if res.Of == 0 || (public && !res.Show()) {
		return nil
	}
	lang := i18n.Locale(ctx)
	n := &ui.NutritionView{Show: res.Show(), Counted: res.Counted, Of: res.Of, Public: public, Credit: i18n.T(ctx, "nutrition.credit")}
	if catalog.NutritionCount() > 0 {
		n.Credit = i18n.T(ctx, "nutrition.creditCatalogue")
	}
	n.Subtitle = pluralKey(ctx, res.Servings, "nutrition.sheet.perServingOne", "nutrition.sheet.perServing")
	if res.Partial() {
		n.Coverage = i18n.T(ctx, "nutrition.coverage", res.Counted, res.Of)
		if public {
			n.Coverage = i18n.T(ctx, "nutrition.coveragePublic", res.Counted, res.Of)
		}
	}
	if n.Show {
		p, k := res.PerServing, res.Known
		n.Kcal = groceries.FormatQuantity(nutrition.RoundKcal(p.Kcal), lang)
		n.PerServing = pluralKey(ctx, res.Servings, "nutrition.perServingOne", "nutrition.perServing")
		if k.Protein {
			n.Protein = grams(ctx, p.Protein)
		}
		if k.Fat {
			n.Fat = grams(ctx, p.Fat)
		}
		if k.Carbs {
			n.Carbs = grams(ctx, p.Carbs)
		}
		if k.Protein && k.Fat && k.Carbs {
			n.Split = ui.EnergySplit(p.Protein, p.Fat, p.Carbs)
		}
		more := func(label i18n.Key, known bool, v float64) {
			if known {
				n.More = append(n.More, ui.NutrientRow{Label: i18n.T(ctx, label), Value: grams(ctx, v)})
			}
		}
		more("nutrition.fatSaturated", k.SatFat, p.SatFat)
		more("nutrition.sugars", k.Sugars, p.Sugars)
		more("nutrition.fibre", k.Fibre, p.Fibre)
		more("nutrition.salt", k.SodiumMg, p.Salt())
	} else if res.Counted == 0 {
		n.NotEnough = i18n.T(ctx, "nutrition.notEnoughNone", res.Of)
	} else {
		n.NotEnough = i18n.T(ctx, "nutrition.notEnoughBody", res.Counted, res.Of)
	}
	if public {
		return n
	}
	if res.OptionalLeftOut > 0 {
		n.LeftOut = pluralKey(ctx, res.OptionalLeftOut, "nutrition.leftOutOne", "nutrition.leftOut")
	}
	next := url.QueryEscape("/recipes/" + recipeID.String())
	for _, m := range res.Missing {
		row := ui.NutritionMissing{Name: m.Name, Amount: amounts[m.Index]}
		item := "/items/" + m.ItemID.String() + "/nutrition?next=" + next
		switch m.Reason {
		case nutrition.ReasonNoData:
			row.Why, row.Href, row.Action = i18n.T(ctx, "nutrition.why.noData"), item, i18n.T(ctx, "nutrition.action.add")
		case nutrition.ReasonNoDensity:
			row.Why, row.Href, row.Action = i18n.T(ctx, "nutrition.why.noDensity"), item, i18n.T(ctx, "nutrition.action.addWeight")
		case nutrition.ReasonNoWeight:
			row.Why, row.Href, row.Action = i18n.T(ctx, "nutrition.why.noWeight"), item, i18n.T(ctx, "nutrition.action.addWeight")
		default:
			row.Why = i18n.T(ctx, "nutrition.why.freeText")
			row.Href, row.Action = "/recipes/"+recipeID.String()+"/edit", i18n.T(ctx, "nutrition.action.editRecipe")
		}
		n.Missing = append(n.Missing, row)
	}
	return n
}

// ---- Item nutrition editor ----

// nutritionFields are the form fields of the editor besides the weights.
var nutritionFields = []string{"kcal", "protein", "fat", "saturated_fat", "carbs", "sugars", "fibre", "salt", "per100ml"}

func weightField(unit string) string { return "weight_" + unit }

var weightLabels = map[string]i18n.Key{
	"piece": "nutrition.weight.piece", "clove": "nutrition.weight.clove", "slice": "nutrition.weight.slice",
	"bunch": "nutrition.weight.bunch", "can": "nutrition.weight.can", "pack": "nutrition.weight.pack",
}

func weightFields(ctx context.Context) []ui.WeightField {
	out := make([]ui.WeightField, len(nutrition.WeightUnits))
	for i, u := range nutrition.WeightUnits {
		out[i] = ui.WeightField{Name: weightField(u), Label: i18n.T(ctx, weightLabels[u])}
	}
	return out
}

// parseDecimal reads a number typed in either decimal notation. Empty input
// is no value.
func parseDecimal(s string) (v *float64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, true
	}
	if strings.Contains(s, ",") {
		if strings.Contains(s, ".") {
			return nil, false
		}
		s = strings.Replace(s, ",", ".", 1)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != f || f > 1e9 || f < -1e9 {
		return nil, false
	}
	return &f, true
}

// nutritionValues fills the form from stored values.
func nutritionValues(ctx context.Context, it nutrition.Item) map[string]string {
	lang := i18n.Locale(ctx)
	vals := map[string]string{}
	set := func(name string, v *float64, scale float64) {
		if v != nil {
			vals[name] = groceries.FormatQuantity(*v*scale, lang)
		}
	}
	if it.Set || it.Catalogue {
		f := it.Facts
		vals["kcal"] = groceries.FormatQuantity(f.Kcal, lang)
		set("protein", f.Protein, 1)
		set("fat", f.Fat, 1)
		set("saturated_fat", f.SatFat, 1)
		set("carbs", f.Carbs, 1)
		set("sugars", f.Sugars, 1)
		set("fibre", f.Fibre, 1)
		set("salt", f.SodiumMg, 1.0/400)
		set("per100ml", f.Density, 100)
	}
	for u, g := range it.Facts.Weights {
		vals[weightField(u)] = groceries.FormatQuantity(g, lang)
	}
	return vals
}

func (s *Server) nutritionEditView(ctx context.Context, it nutrition.Item, next string, f ui.Form) ui.NutritionEditView {
	if f.Values == nil {
		f.Values = nutritionValues(ctx, it)
	}
	v := ui.NutritionEditView{
		ID: it.ID.String(), Name: it.Name, Next: next, Has: it.Set, Form: f, Units: weightFields(ctx),
		Used:       pluralKey(ctx, it.Recipes, "nutrition.usedOne", "nutrition.used"),
		ClearLabel: i18n.T(ctx, "nutrition.clear"),
	}
	switch {
	case it.Catalogue && it.Set:
		v.Note = i18n.T(ctx, "nutrition.ownValues")
		v.ClearLabel = i18n.T(ctx, "nutrition.resetCatalogue")
	case it.Catalogue:
		v.Note = i18n.T(ctx, "nutrition.fromCatalogue")
	}
	return v
}

func (s *Server) renderNutritionEdit(w http.ResponseWriter, r *http.Request, status int, it nutrition.Item, next string, f ui.Form) {
	ctx := r.Context()
	p := s.page(r, i18n.T(ctx, "nutrition.edit.title", it.Name), ui.NavRecipes)
	p.BackHref, p.BackLabel = next, i18n.T(ctx, "nutrition.back")
	s.render(w, r, status, ui.NutritionEditPage(p, s.nutritionEditView(ctx, it, next, f)))
}

func nutritionNext(r *http.Request) string {
	v := r.FormValue("next")
	if r.Method != http.MethodGet {
		v = r.PostFormValue("next")
	}
	return localPath(v, "/recipes")
}

func (s *Server) nutritionPage(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	it, err := s.nutrition.Get(r.Context(), sc, id)
	if errors.Is(err, nutrition.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderNutritionEdit(w, r, http.StatusOK, it, nutritionNext(r), ui.Form{})
}

func (s *Server) saveNutrition(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	names := append([]string{}, nutritionFields...)
	for _, u := range nutrition.WeightUnits {
		names = append(names, weightField(u))
	}
	f := formOf(r, names...)
	var in nutrition.Input
	bad := func(name string) { f.Errors[name] = i18n.T(ctx, "nutrition.error.number") }
	num := func(name string) *float64 {
		v, ok := parseDecimal(f.Value(name))
		if !ok {
			bad(name)
		}
		return v
	}
	if kcal := num("kcal"); kcal != nil {
		in.Kcal = *kcal
	} else if f.Errors["kcal"] == "" {
		f.Errors["kcal"] = i18n.T(ctx, "form.error.required")
	}
	in.Protein, in.Fat, in.SatFat = num("protein"), num("fat"), num("saturated_fat")
	in.Carbs, in.Sugars, in.Fibre, in.SaltG = num("carbs"), num("sugars"), num("fibre"), num("salt")
	if ml := num("per100ml"); ml != nil {
		d := *ml / 100
		in.Density = &d
	}
	in.Weights = map[string]float64{}
	for _, u := range nutrition.WeightUnits {
		if g := num(weightField(u)); g != nil {
			in.Weights[u] = *g
		}
	}
	next := nutritionNext(r)
	it, err := s.nutrition.Get(ctx, sc, id)
	if errors.Is(err, nutrition.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(f.Errors) == 0 {
		err = s.nutrition.Save(ctx, sc, identity(r).UserID, id, in)
		if errors.Is(err, nutrition.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		if err == nil {
			seeOther(w, r, next)
			return
		}
		fe, ok := withErrors(ctx, f, err)
		if !ok {
			s.serverError(w, r, err)
			return
		}
		// A weight's error belongs to its form field.
		f = ui.Form{Values: fe.Values, Errors: map[string]string{}}
		for field, msg := range fe.Errors {
			if unit, found := strings.CutPrefix(field, "weight."); found {
				field = weightField(unit)
			} else if field == "density" {
				field = "per100ml"
			}
			f.Errors[field] = msg
		}
	}
	s.renderNutritionEdit(w, r, http.StatusUnprocessableEntity, it, next, f)
}

func (s *Server) clearNutrition(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	err := s.nutrition.Clear(r.Context(), sc, id)
	if errors.Is(err, nutrition.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, nutritionNext(r))
}

// nutritionOf estimates a recipe at its own servings.
func (s *Server) nutritionOf(ctx context.Context, sc store.Scoped, rec recipes.Recipe) (nutrition.Result, error) {
	return s.nutrition.Recipe(ctx, sc, nutritionLines(rec), rec.Servings)
}
