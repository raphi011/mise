package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// Public recipe links. Members create and turn off a recipe's link from its
// menu; anyone with the link reads the recipe at /s/{token}, without a
// session.

// sharePrefix starts every public link route. Requests under it skip the
// session middleware, so a member's identity never reaches them.
const sharePrefix = "/s/"

func isShare(path string) bool { return strings.HasPrefix(path, sharePrefix) }

// publicURL is the absolute address of a public link.
func (s *Server) publicURL(r *http.Request, token string) string {
	base := s.BaseURL()
	if base == nil {
		u, err := url.Parse(requestOrigin(r, s.cfg.TrustedProxies))
		if err != nil {
			return sharePrefix + token
		}
		base = u
	}
	return base.JoinPath("s", token).String()
}

// menuView is the recipe menu for the signed-in viewer.
func (s *Server) menuView(r *http.Request, sc store.Scoped, rec recipes.Recipe) (ui.RecipeMenuView, error) {
	ctx := r.Context()
	id := identity(r)
	v := ui.RecipeMenuView{
		ID: rec.ID.String(), Name: rec.Name, ByLabel: personLabel(ctx, rec.CreatedBy, id.UserID), ShareURL: s.shareURL(r, rec.ID),
	}
	sh, ok, err := s.recipes.Share(ctx, sc, rec.ID)
	if err != nil {
		return v, err
	}
	if ok {
		v.PublicURL = s.publicURL(r, sh.Token)
		v.PublicBy = i18n.T(ctx, "recipes.menu.publicBy", personLabel(ctx, sh.CreatedBy, id.UserID), dayMonth(ctx, sh.CreatedAt, location(id)))
	}
	return v, nil
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	_, err := s.recipes.CreateShare(r.Context(), sc, identity(r).UserID, recipeID)
	s.respondShare(w, r, sc, recipeID, err, "recipes.toast.publicOn")
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	recipeID, sc, ok := s.recipeTarget(w, r)
	if !ok {
		return
	}
	err := s.recipes.RevokeShare(r.Context(), sc, recipeID)
	s.respondShare(w, r, sc, recipeID, err, "recipes.toast.publicOff")
}

// respondShare answers a create or revoke: Datastar gets the menu's public
// link entry and a toast; a form post goes back to the menu page, which
// shows the new state.
func (s *Server) respondShare(w http.ResponseWriter, r *http.Request, sc store.Scoped, recipeID uuid.UUID, err error, msg i18n.Key) {
	if errors.Is(err, recipes.ErrNotFound) {
		s.recipeGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !isDatastar(r) {
		seeOther(w, r, "/recipes/"+recipeID.String()+"/menu")
		return
	}
	ctx := r.Context()
	rec, err := s.recipes.Get(ctx, sc, recipeID, identity(r).UserID)
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
	s.sendPatches(w, r, patchTempl(ui.PublicLink(v)), toast(i18n.T(ctx, msg), "", nil))
}

// shared wraps the public link routes: headers for a page anyone may open,
// then the token resolved to its recipe (or the "link doesn't work" page).
func (s *Server) shared(h http.HandlerFunc) http.Handler {
	resolved := s.auth.RequireShare(http.HandlerFunc(s.shareGone))(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		// Keep the link out of search engines and archives, and out of the
		// Referer of anything the page links to (the source site).
		hd.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		hd.Set("Referrer-Policy", "no-referrer")
		// Revoking must take effect at once: no shared cache may keep the
		// page, and browsers ask again each time.
		hd.Set("Cache-Control", "no-store")
		resolved.ServeHTTP(w, r)
	})
}

// shareGone is the 404 of a public link: a page without the app shell, so
// it neither leads into the app nor says whether the link ever existed.
func (s *Server) shareGone(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := ui.Page{Title: i18n.T(ctx, "shared.gone.title"), Public: true}
	s.render(w, r, http.StatusNotFound, ui.SharedGonePage(p))
}

// shareTarget returns the resolved link and a scope for its household.
func (s *Server) shareTarget(w http.ResponseWriter, r *http.Request) (auth.Share, store.Scoped, bool) {
	sh, ok := auth.ShareFromContext(r.Context())
	if !ok {
		s.shareGone(w, r)
		return auth.Share{}, store.Scoped{}, false
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return auth.Share{}, store.Scoped{}, false
	}
	return sh, sc, true
}

func (s *Server) sharedRecipePage(w http.ResponseWriter, r *http.Request) {
	sh, sc, ok := s.shareTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	rec, err := s.recipes.Public(ctx, sc, sh.RecipeID)
	if errors.Is(err, recipes.ErrNotFound) {
		s.shareGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	token := r.PathValue("token")
	v := ui.SharedRecipeView{
		Tint: ui.Tint(rec.ID.String()), Name: rec.Name, Description: rec.Description,
		Prep: minutes(ctx, rec.PrepMin), Cook: minutes(ctx, rec.CookMin), Servings: rec.Servings,
	}
	if rec.Photo != "" {
		v.PhotoURL = sharePrefix + token + "/photo/full"
	}
	v.SourceURL, v.SourceHost = sourceLink(rec.SourceURL)
	sys := viewerSystem(r)
	amounts := scaledAmounts(ctx, rec, rec.Servings, sys)
	for i, ing := range rec.Ingredients {
		v.Ingredients = append(v.Ingredients, ui.IngredientView{Name: ing.ItemName, Amount: amounts[i], Note: ing.Note, Optional: ing.Optional})
	}
	v.Steps = stepParts(rec, amounts, sys, firstInMethod)
	est, err := s.nutritionOf(ctx, sc, rec)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Nutrition = nutritionBlock(ctx, rec.ID, est, nil, true)
	page := ui.Page{Title: rec.Name, Public: true, Description: ui.SharedSummary(ctx, rec.Description, v)}
	if v.PhotoURL != "" {
		page.Image = s.publicOrigin(r) + v.PhotoURL
	}
	s.render(w, r, http.StatusOK, ui.SharedRecipePage(page, v))
}

// sharedPhoto serves the shared recipe's current photo, and nothing else:
// the route takes no image hash, so a link cannot reach the household's
// other images.
func (s *Server) sharedPhoto(w http.ResponseWriter, r *http.Request) {
	sh, sc, ok := s.shareTarget(w, r)
	if !ok {
		return
	}
	size := images.Size(r.PathValue("size"))
	rec, err := s.recipes.Public(r.Context(), sc, sh.RecipeID)
	if errors.Is(err, recipes.ErrNotFound) || (err == nil && rec.Photo == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := s.images.Open(sh.HouseholdID, rec.Photo, size)
	if errors.Is(err, images.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "image/jpeg")
	// Unlike the members' image route, not immutable: the browser checks
	// back every time (a cheap 304 by ETag), so a turned-off link stops
	// serving the photo too.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", `"`+rec.Photo+"-"+string(size)+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}

// viewerSystem picks the unit system for a visitor without an account: US
// when their browser's first language names the United States (or another
// country using US customary units), metric otherwise.
func viewerSystem(r *http.Request) groceries.System {
	tags, _, err := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if err != nil || len(tags) == 0 {
		return groceries.Metric
	}
	region, conf := tags[0].Region()
	if conf != language.Exact {
		return groceries.Metric
	}
	switch region.String() {
	case "US", "LR", "MM":
		return groceries.US
	}
	return groceries.Metric
}

// publicOrigin is the server's public address without a trailing slash: the
// configured base URL, else the origin the request came in on.
func (s *Server) publicOrigin(r *http.Request) string {
	if base := s.BaseURL(); base != nil {
		return strings.TrimRight(base.String(), "/")
	}
	return requestOrigin(r, s.cfg.TrustedProxies)
}
