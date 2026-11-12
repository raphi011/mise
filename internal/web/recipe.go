package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

var monthKeys = [12]i18n.Key{
	"date.month.1", "date.month.2", "date.month.3", "date.month.4", "date.month.5", "date.month.6",
	"date.month.7", "date.month.8", "date.month.9", "date.month.10", "date.month.11", "date.month.12",
}

var shortMonthKeys = [12]i18n.Key{
	"date.monthShort.1", "date.monthShort.2", "date.monthShort.3", "date.monthShort.4", "date.monthShort.5", "date.monthShort.6",
	"date.monthShort.7", "date.monthShort.8", "date.monthShort.9", "date.monthShort.10", "date.monthShort.11", "date.monthShort.12",
}

// monthYear formats t as "March 2026" in the household's zone.
func monthYear(ctx context.Context, t time.Time, loc *time.Location) string {
	t = t.In(loc)
	return monthName(ctx, t.Year(), t.Month())
}

// monthName is "March 2026". The year is passed as text: the locale's
// number format would group its digits ("2,026").
func monthName(ctx context.Context, year int, month time.Month) string {
	return i18n.T(ctx, "date.monthYear", i18n.T(ctx, monthKeys[month-1]), strconv.Itoa(year))
}

// dayMonth formats t as "24 Oct" in the household's zone.
func dayMonth(ctx context.Context, t time.Time, loc *time.Location) string {
	t = t.In(loc)
	return i18n.T(ctx, "date.dayMonth", t.Day(), i18n.T(ctx, shortMonthKeys[t.Month()-1]))
}

// joinNames lists names as "Ben", "Ben and Chris", "Ben, Chris and Dana".
func joinNames(ctx context.Context, names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return i18n.T(ctx, "list.and", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// sourceLink returns a source URL safe to link to and its host, or "" for
// anything that is not http(s).
func sourceLink(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ""
	}
	return u.String(), strings.TrimPrefix(u.Hostname(), "www.")
}

// clampServings returns servings when it is a valid stepper value, else
// the recipe's own.
func clampServings(servings, base int) int {
	if servings < 1 || servings > ui.MaxServings {
		return base
	}
	return servings
}

// recipeView loads a recipe's detail region for the viewer at servings. A
// recipe that is gone renders as such.
func (s *Server) recipeView(ctx context.Context, sc store.Scoped, id *auth.Identity, recipeID uuid.UUID, servings int) (ui.RecipeView, error) {
	rec, err := s.recipes.Get(ctx, sc, recipeID, id.UserID)
	if errors.Is(err, recipes.ErrNotFound) {
		return ui.RecipeView{ID: recipeID.String(), Gone: true}, nil
	}
	if err != nil {
		return ui.RecipeView{}, err
	}
	st, err := s.recipes.ListStatus(ctx, sc, rec.Ingredients)
	if err != nil {
		return ui.RecipeView{}, err
	}
	loc := location(id)
	servings = clampServings(servings, rec.Servings)
	v := ui.RecipeView{
		ID: rec.ID.String(), Name: rec.Name, Description: rec.Description, Photo: rec.Photo,
		Prep: minutes(ctx, rec.PrepMin), Cook: minutes(ctx, rec.CookMin),
		Servings: servings, Base: rec.Servings,
		By:     ui.Viewer{Name: rec.CreatedBy.Name, Color: rec.CreatedBy.Color},
		ListID: st.List.ID.String(), ListName: st.List.Name,
		Notes: rec.Notes, Favourite: rec.Favourite, Shared: rec.Shared,
	}
	v.SourceURL, v.SourceHost = sourceLink(rec.SourceURL)
	for _, l := range rec.Links {
		v.Links = append(v.Links, ui.LinkView{ID: l.ID.String(), Name: l.Name, Photo: l.Photo, Meta: minutes(ctx, l.TotalMin)})
	}
	for _, t := range rec.Tags {
		v.Tags = append(v.Tags, t.Name)
	}
	switch rec.TimesCooked {
	case 0:
		v.Cooked = i18n.T(ctx, "recipes.cookedNever")
	case 1:
		v.Cooked = i18n.T(ctx, "recipes.cookedOnce")
	default:
		v.Cooked = i18n.T(ctx, "recipes.cookedTimes", rec.TimesCooked)
	}
	v.Attribution = i18n.T(ctx, "recipes.attribution", personLabel(ctx, rec.CreatedBy, id.UserID), monthYear(ctx, rec.CreatedAt, loc))
	if rec.UpdatedBy.ID != rec.CreatedBy.ID {
		v.Attribution += " · " + i18n.T(ctx, "recipes.lastEdited", personLabel(ctx, rec.UpdatedBy, id.UserID))
	}
	if rec.NotesBy != nil && rec.NotesAt != nil {
		v.NotesBy = &ui.Viewer{Name: personLabel(ctx, *rec.NotesBy, id.UserID), Color: rec.NotesBy.Color}
		v.NotesWhen = dayMonth(ctx, *rec.NotesAt, loc)
	}

	est, err := s.nutritionOf(ctx, sc, rec)
	if err != nil {
		return ui.RecipeView{}, err
	}
	sys := unitSystem(id)
	v.Nutrition = nutritionBlock(ctx, rec.ID, est, scaledAmounts(ctx, rec, rec.Servings, sys), false)

	amounts := scaledAmounts(ctx, rec, servings, sys)
	for i, ing := range rec.Ingredients {
		v.Ingredients = append(v.Ingredients, ui.IngredientView{
			ID: ing.ID.String(), Name: ing.ItemName, Amount: amounts[i], Note: ing.Note, Optional: ing.Optional, On: st.On[ing.ItemID],
			Uncounted: est.Lines[i].Uncounted,
		})
	}
	on, total := st.Count(rec.Ingredients)
	v.ListStatus = i18n.T(ctx, "recipes.onList", on, total, st.List.Name)

	v.Steps = stepParts(rec, amounts, sys, firstInMethod)
	return v, nil
}

// scaledAmounts returns the display amount of each ingredient at servings
// in the unit system sys. Amounts scale in the unit they were written in and
// convert afterwards, so the converted unit fits the scaled quantity.
func scaledAmounts(ctx context.Context, rec recipes.Recipe, servings int, sys groceries.System) []string {
	lang := i18n.Locale(ctx)
	amounts := make([]string, len(rec.Ingredients))
	for i, ing := range rec.Ingredients {
		scaled := recipes.Scale(ing.Amount, rec.Servings, servings, lang)
		amounts[i] = recipes.DisplayAmount(groceries.Convert(scaled, sys, lang), lang)
	}
	return amounts
}

// mentionScope says which mention of an ingredient stepParts highlights.
type mentionScope int

const (
	// firstInMethod highlights only the first mention in the whole method,
	// which the detail page shows as one text.
	firstInMethod mentionScope = iota
	// firstInStep highlights the first mention in each step, for cook mode,
	// which shows one step per screen.
	firstInStep
)

// stepParts splits each step into text and ingredient mentions carrying
// their amounts. Temperatures in the text are shown in the scale of sys.
func stepParts(rec recipes.Recipe, amounts []string, sys groceries.System, scope mentionScope) [][]ui.StepPart {
	seen := map[int]bool{}
	out := make([][]ui.StepPart, 0, len(rec.Steps))
	for _, step := range rec.Steps {
		if scope == firstInStep {
			clear(seen)
		}
		var parts []ui.StepPart
		for _, p := range recipes.Highlight(step.Text, rec.Ingredients, seen) {
			part := ui.StepPart{Text: p.Text}
			if p.Ingredient >= 0 {
				part.Mention, part.Amount, part.Icon = true, amounts[p.Ingredient], rec.Ingredients[p.Ingredient].Icon
			} else {
				part.Text = recipes.ConvertTemperatures(p.Text, sys)
			}
			parts = append(parts, part)
		}
		out = append(out, parts)
	}
	return out
}

// recipeTarget parses the {id} path value and the scope.
func (s *Server) recipeTarget(w http.ResponseWriter, r *http.Request) (uuid.UUID, store.Scoped, bool) {
	return s.groceriesTarget(w, r)
}

func servingsParam(r *http.Request, name string) int {
	v := r.FormValue(name)
	if r.Method != http.MethodGet {
		v = r.PostFormValue(name)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func (s *Server) recipePage(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := s.recipeView(ctx, sc, identity(r), recipeID, servingsParam(r, "servings"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Gone {
		s.notFound(w, r)
		return
	}
	if event, ok := s.takeCookedFlash(w, r, recipeID); ok {
		t := cookedToast(ctx, recipeID, event, r.URL.RequestURI())
		v.Toast = &t
	}
	if s.takeImportedFlash(w, r, recipeID) {
		v.Toast = &ui.Toast{Key: strconv.FormatInt(time.Now().UnixNano(), 36), Message: i18n.T(ctx, "import.toast.noPhoto")}
	}
	p := s.page(r, v.Name, ui.NavRecipes)
	p.BackHref, p.BackLabel = "/recipes", i18n.T(ctx, "recipes.back")
	p.Actions = ui.RecipeActions(v)
	s.render(w, r, http.StatusOK, ui.RecipePage(p, v))
}

// respondRecipe answers a successful action on a recipe: Datastar gets the
// viewer's region at servings and the extra patches; a plain form post is
// redirected to the recipe.
func (s *Server) respondRecipe(w http.ResponseWriter, r *http.Request, sc store.Scoped, recipeID uuid.UUID, servings int, extra ...patch) {
	ctx := r.Context()
	v, err := s.recipeView(ctx, sc, identity(r), recipeID, servings)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !isDatastar(r) {
		if v.Gone {
			seeOther(w, r, "/recipes")
			return
		}
		seeOther(w, r, v.ServingsHref(v.Servings))
		return
	}
	patches := []patch{patchTempl(ui.RecipeRegion(v))}
	if !v.Gone {
		patches = append(patches, patchTempl(ui.RecipeFavourite(v)))
	}
	s.sendPatches(w, r, append(patches, extra...)...)
}

// recipeGone answers an action on a recipe or ingredient that no longer
// exists.
func (s *Server) recipeGone(w http.ResponseWriter, r *http.Request) {
	if !isDatastar(r) {
		s.notFound(w, r)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.EmptySheet()), toast(i18n.T(r.Context(), "recipes.toast.gone"), "", nil))
}

func (s *Server) toggleIngredient(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ingID, err := uuid.Parse(r.PathValue("ing"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	on, err := strconv.ParseBool(r.PostFormValue("on"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	servings := servingsParam(r, "servings")
	t, err := s.recipes.ToggleIngredient(ctx, sc, identity(r).UserID, recipeID, ingID, servings, on, i18n.Locale(ctx))
	if errors.Is(err, recipes.ErrNotFound) || errors.Is(err, groceries.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var msg patch
	switch {
	case on:
		msg = toast(i18n.T(ctx, "recipes.toast.added", t.ItemName, t.List.Name), "", nil)
	case len(t.Removed) > 0:
		ids := make([]string, len(t.Removed))
		for i, id := range t.Removed {
			ids[i] = id.String()
		}
		msg = toast(i18n.T(ctx, "recipes.toast.removed", t.ItemName, t.List.Name),
			"/recipes/"+recipeID.String()+"/ingredients/restore",
			map[string]string{"entries": strings.Join(ids, ","), "servings": strconv.Itoa(servings)})
	default:
		msg = clearToast
	}
	s.respondRecipe(w, r, sc, recipeID, servings, msg)
}

// restoreIngredients is the Undo of taking an ingredient off the list.
func (s *Server) restoreIngredients(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	for _, raw := range strings.Split(r.PostFormValue("entries"), ",") {
		id, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		// An entry restored elsewhere, or past the Undo window, stays as
		// it is.
		if _, err := s.groceries.Restore(ctx, sc, id); err != nil && !errors.Is(err, groceries.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
	}
	s.respondRecipe(w, r, sc, recipeID, servingsParam(r, "servings"), clearToast)
}

func (s *Server) setFavourite(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	on, err := strconv.ParseBool(r.PostFormValue("on"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	err = s.recipes.SetFavourite(r.Context(), sc, identity(r).UserID, recipeID, on)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !isDatastar(r) {
		seeOther(w, r, ui.RecipeHref(recipeID.String()))
		return
	}
	s.sendPatches(w, r, patchTempl(ui.RecipeFavourite(ui.RecipeView{ID: recipeID.String(), Favourite: on})))
}

// shareURL is the recipe's address for people signed in to this server.
func (s *Server) shareURL(r *http.Request, recipeID uuid.UUID) string {
	base := s.BaseURL()
	if base == nil {
		u, err := url.Parse(requestOrigin(r, s.cfg.TrustedProxies))
		if err != nil {
			return ui.RecipeHref(recipeID.String())
		}
		base = u
	}
	return base.JoinPath("recipes", recipeID.String()).String()
}

func (s *Server) recipeMenu(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	id := identity(r)
	rec, err := s.recipes.Get(ctx, sc, recipeID, id.UserID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.menuView(r, sc, rec)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.RecipeMenuSheet(v)))
		return
	}
	p := s.page(r, rec.Name, ui.NavRecipes)
	p.BackHref, p.BackLabel = ui.RecipeHref(v.ID), i18n.T(ctx, "recipes.backToRecipe")
	s.render(w, r, http.StatusOK, ui.RecipeMenuPage(p, v))
}

// nutritionSheetView loads a recipe's nutrition popup, or nil when the
// recipe has none to show (no ingredients with an amount).
func (s *Server) nutritionSheetView(ctx context.Context, sc store.Scoped, id *auth.Identity, recipeID uuid.UUID) (*ui.NutritionSheetView, error) {
	rec, err := s.recipes.Get(ctx, sc, recipeID, id.UserID)
	if err != nil {
		return nil, err
	}
	est, err := s.nutritionOf(ctx, sc, rec)
	if err != nil {
		return nil, err
	}
	n := nutritionBlock(ctx, rec.ID, est, scaledAmounts(ctx, rec, rec.Servings, unitSystem(id)), false)
	if n == nil {
		return nil, nil
	}
	return &ui.NutritionSheetView{ID: rec.ID.String(), Nutrition: *n}, nil
}

// recipeNutrition is the nutrition popup: a dialog for Datastar, a page
// without JavaScript.
func (s *Server) recipeNutrition(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := s.nutritionSheetView(ctx, sc, identity(r), recipeID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if v == nil {
		if isDatastar(r) {
			s.sendPatches(w, r, patchTempl(ui.EmptySheet()))
			return
		}
		seeOther(w, r, ui.RecipeHref(recipeID.String()))
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.NutritionSheet(*v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "nutrition.title"), ui.NavRecipes)
	p.BackHref, p.BackLabel = ui.RecipeHref(v.ID), i18n.T(ctx, "recipes.backToRecipe")
	s.render(w, r, http.StatusOK, ui.NutritionPage(p, *v))
}

func (s *Server) deleteRecipePage(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	info, err := s.recipes.DeleteInfo(ctx, sc, recipeID, identity(r).UserID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var body []string
	switch info.TimesCooked {
	case 0:
	case 1:
		body = append(body, i18n.T(ctx, "recipes.delete.cookedOnce"))
	default:
		body = append(body, i18n.T(ctx, "recipes.delete.cooked", info.TimesCooked))
	}
	planned, err := s.plan.PlannedFor(ctx, sc, recipeID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if line := plannedLine(ctx, planned); line != "" {
		body = append([]string{line}, body...)
	}
	if len(info.Others) > 0 {
		names := make([]string, len(info.Others))
		for i, p := range info.Others {
			names[i] = p.Name
		}
		body = append(body, i18n.T(ctx, "recipes.delete.others", joinNames(ctx, names)))
	}
	body = append(body, i18n.T(ctx, "recipes.delete.listStays"))
	v := ui.DeleteRecipeView{ID: recipeID.String(), Name: info.Name, Body: strings.Join(body, " ")}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.DeleteRecipeDialog(v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "recipes.delete.pageTitle"), ui.NavRecipes)
	p.BackHref, p.BackLabel = ui.RecipeHref(v.ID), i18n.T(ctx, "recipes.backToRecipe")
	s.render(w, r, http.StatusOK, ui.DeleteRecipePage(p, v))
}

// plannedLine is the delete confirmation's "It's on the plan for …", or
// "" when the recipe is not planned from today on.
func plannedLine(ctx context.Context, dates []store.Date) string {
	const shown = 3
	if len(dates) == 0 {
		return ""
	}
	var days []string
	for i, d := range dates {
		if i == shown {
			days = append(days, plural(ctx, len(dates)-shown, "recipes.delete.plannedMoreOne", "recipes.delete.plannedMore"))
			break
		}
		days = append(days, shortDate(ctx, d))
	}
	return i18n.T(ctx, "recipes.delete.planned", joinNames(ctx, days))
}

// redirect sends the browser to target: Datastar by script, a form post by
// 303.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target string) {
	if !isDatastar(r) {
		seeOther(w, r, target)
		return
	}
	s.sendPatches(w, r, func(sse *datastar.ServerSentEventGenerator) error { return sse.Redirect(target) })
}

func (s *Server) deleteRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	err := s.recipes.Delete(r.Context(), sc, recipeID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/recipes?deleted="+recipeID.String())
}

func (s *Server) restoreRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	err := s.recipes.Restore(r.Context(), sc, recipeID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, ui.RecipeHref(recipeID.String()))
}

// nutritionPatch keeps an open nutrition popup current: its body is
// re-rendered, or the popup closed when the recipe no longer has numbers to
// show.
func nutritionPatch(sse *datastar.ServerSentEventGenerator, v ui.RecipeView, open bool) error {
	if !open {
		return nil
	}
	if v.Gone || v.Nutrition == nil {
		// Morphing the dialog away fires no close event, so reset the signal here.
		if err := sse.MarshalAndPatchSignals(map[string]any{"nutOpen": false}); err != nil {
			return err
		}
		return sse.PatchElementTempl(ui.EmptySheet())
	}
	return sse.PatchElementTempl(ui.NutritionSheetBody(ui.NutritionSheetView{ID: v.ID, Nutrition: *v.Nutrition}, true))
}

// recipeLive re-renders a recipe page at the viewer's servings (a signal
// sent when the stream opens) when the recipe, the default list or the
// catalogue change.
func (s *Server) recipeLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	recipeID, err := uuid.Parse(r.URL.Query().Get("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	var sig struct {
		Servings int  `json:"servings"`
		NutOpen  bool `json:"nutOpen"`
	}
	if isDatastar(r) {
		if err := datastar.ReadSignals(r, &sig); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}
	// list is the default list as of the last render; Refresh and Relevant
	// run on the stream's goroutine.
	var list string
	s.serveLive(w, r, id, live.Stream{
		Refresh: func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			v, err := s.recipeView(ctx, sc, id, recipeID, sig.Servings)
			if err != nil {
				return err
			}
			list = v.ListID
			if err := sse.PatchElementTempl(ui.RecipeRegion(v)); err != nil {
				return err
			}
			if v.Gone {
				return nutritionPatch(sse, v, sig.NutOpen)
			}
			if err := sse.PatchElementTempl(ui.PageTitle(v.Name)); err != nil {
				return err
			}
			if err := sse.PatchElementTempl(ui.RecipeFavourite(v)); err != nil {
				return err
			}
			return nutritionPatch(sse, v, sig.NutOpen)
		},
		Relevant: func(e live.Event) bool {
			switch e.Kind {
			case live.RecipeChanged:
				return e.RecipeID == recipeID
			case live.ListChanged:
				return e.ListID.String() == list
			case live.ListsChanged, live.CatalogChanged, live.TagsChanged:
				return true
			}
			return false
		},
	})
}
