package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/ui"
)

// cookedCookie carries the cook event a plain form post just recorded to
// the recipe page, which shows its Undo toast once. Cook mode's Done is
// such a post: it leaves the page.
const cookedCookie = "mise_cooked"

// cookPage is cook mode: the method one step per screen at the servings
// the detail page showed.
func (s *Server) cookPage(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rec, err := s.recipes.Get(ctx, sc, recipeID, identity(r).UserID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	servings := clampServings(servingsParam(r, "servings"), rec.Servings)
	v := ui.CookView{ID: rec.ID.String(), Name: rec.Name, Servings: servings, Base: rec.Servings}
	if len(rec.Steps) == 0 {
		http.Redirect(w, r, v.RecipeBack(), http.StatusFound)
		return
	}
	for i, parts := range stepParts(rec, scaledAmounts(ctx, rec, servings)) {
		st := ui.CookStep{Parts: parts}
		if t := rec.Steps[i].TimerSeconds; t != nil {
			st.TimerSeconds = *t
		}
		v.Steps = append(v.Steps, st)
	}
	if n, err := strconv.Atoi(r.FormValue("step")); err == nil {
		v.Step = min(max(n, 1), len(v.Steps)) - 1
	}
	p := s.page(r, rec.Name, ui.NavRecipes)
	p.Theme = "dark"
	s.render(w, r, http.StatusOK, ui.CookPage(p, v))
}

// isRecipePage reports whether path is the recipe's detail page.
func isRecipePage(path string, recipeID uuid.UUID) bool {
	rest, ok := strings.CutPrefix(path, ui.RecipeHref(recipeID.String()))
	return ok && (rest == "" || rest[0] == '?' || rest[0] == '#')
}

// cookedToast is the Undo toast of a recorded cook event.
func cookedToast(ctx context.Context, recipeID, event uuid.UUID, back string) ui.Toast {
	return ui.Toast{
		Key:        event.String(),
		Message:    i18n.T(ctx, "plan.toast.cooked"),
		UndoAction: "/recipes/" + recipeID.String() + "/cooked/" + event.String() + "/undo",
		UndoFields: map[string]string{"back": back},
	}
}

// setCookedFlash remembers a cook event for the recipe page's Undo toast.
func (s *Server) setCookedFlash(w http.ResponseWriter, r *http.Request, recipeID, event uuid.UUID) {
	http.SetCookie(w, &http.Cookie{
		Name: cookedCookie, Value: event.String(), Path: ui.RecipeHref(recipeID.String()), MaxAge: 60,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
}

// takeCookedFlash returns and clears the cook event remembered for the
// recipe page, if any.
func (s *Server) takeCookedFlash(w http.ResponseWriter, r *http.Request, recipeID uuid.UUID) (uuid.UUID, bool) {
	c, err := r.Cookie(cookedCookie)
	if err != nil {
		return uuid.Nil, false
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookedCookie, Path: ui.RecipeHref(recipeID.String()), MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
	event, err := uuid.Parse(c.Value)
	return event, err == nil
}
