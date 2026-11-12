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
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// cookedCookie carries the cook event a plain form post just recorded to
// the recipe page, which shows its Undo toast once. Cook mode's Done is
// such a post: it leaves the page. Its value is "<recipe ID>.<event ID>" and
// its path /recipes, since the page's address is its slug, which may differ
// from the address the post came from.
const cookedCookie = "mise_cooked"

// flashPath is the path of the cookies a recipe page reads once; their
// value names the recipe they are for.
const flashPath = "/recipes"

// cookPage is cook mode: the method one step per screen at the servings
// the detail page showed.
func (s *Server) cookPage(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipePageTarget(w, r)
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
	v := ui.CookView{ID: rec.ID.String(), Slug: rec.Slug, Name: rec.Name, Servings: servings, Base: rec.Servings}
	if len(rec.Steps) == 0 {
		http.Redirect(w, r, v.RecipeBack(), http.StatusFound)
		return
	}
	sys := unitSystem(identity(r))
	for i, parts := range stepParts(rec, scaledAmounts(ctx, rec, servings, sys), sys, firstInStep) {
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

// isRecipePage reports whether path is the recipe's detail page, at any of
// its addresses (see recipes.Resolve).
func (s *Server) isRecipePage(ctx context.Context, sc store.Scoped, path string, recipeID uuid.UUID) (bool, error) {
	handle, ok := strings.CutPrefix(path, ui.RecipeHref(""))
	if !ok {
		return false, nil
	}
	if i := strings.IndexAny(handle, "?#"); i >= 0 {
		handle = handle[:i]
	}
	if strings.Contains(handle, "/") {
		return false, nil
	}
	id, _, err := s.recipes.Resolve(ctx, sc, handle)
	if errors.Is(err, recipes.ErrNotFound) {
		return false, nil
	}
	return err == nil && id == recipeID, err
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
		Name: cookedCookie, Value: recipeID.String() + "." + event.String(), Path: flashPath, MaxAge: 60,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
}

// takeCookedFlash returns and clears the cook event remembered for the
// recipe page, if any. A cookie for another recipe stays.
func (s *Server) takeCookedFlash(w http.ResponseWriter, r *http.Request, recipeID uuid.UUID) (uuid.UUID, bool) {
	c, err := r.Cookie(cookedCookie)
	if err != nil {
		return uuid.Nil, false
	}
	recipe, rawEvent, _ := strings.Cut(c.Value, ".")
	if recipe != recipeID.String() {
		return uuid.Nil, false
	}
	s.clearFlash(w, r, cookedCookie)
	event, err := uuid.Parse(rawEvent)
	return event, err == nil
}

// clearFlash deletes a recipe page's one-time cookie.
func (s *Server) clearFlash(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: flashPath, MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode,
	})
}
