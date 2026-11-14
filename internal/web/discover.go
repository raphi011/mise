package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/discover"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// discoverRefresh is how often the search page and the preview reload
// themselves without JavaScript while a hit is importing.
const discoverRefresh = 3

// webSearch reads the web part of a search from a request's q, site and
// wp values. An unknown site searches all of them.
func (s *Server) webSearch(q, site, pages string) ui.WebSearch {
	n, _ := strconv.Atoi(pages)
	w := ui.WebSearch{Query: discover.Query(q), Pages: discover.Pages(n)}
	if s.discover.HasSite(site) {
		w.Site = site
	}
	return w
}

// webBase is the "From the web" section for w before anything is
// searched: its chips and heading, and whether the query is too short.
func (s *Server) webBase(w ui.WebSearch) ui.WebView {
	v := ui.WebView{Search: w}
	switch sites := s.discover.Sites(); len(sites) {
	case 0:
	case 1:
		v.Site = sites[0]
	default:
		for _, name := range sites {
			v.Sites = append(v.Sites, ui.WebSite{Name: name})
		}
	}
	if w.Query != "" && !discover.Searchable(w.Query) {
		v.Short = discover.MinQuery
	}
	return v
}

// webView searches the sites for w (fetching what is not cached) and
// marks each hit for user: in the library or being imported.
func (s *Server) webView(ctx context.Context, sc store.Scoped, user uuid.UUID, w ui.WebSearch) (ui.WebView, error) {
	v := s.webBase(w)
	if w.Query == "" || v.Short > 0 {
		return v, nil
	}
	res := s.discover.Search(ctx, w.Query, w.Site, w.Pages)
	if res.Err != nil {
		s.log.WarnContext(ctx, "site search", slog.Any("err", res.Err))
	}
	v.More = res.More
	v.Unavailable = strings.Join(res.Unavailable, ", ")
	for i := range v.Sites {
		v.Sites[i].Down = slices.Contains(res.Unavailable, v.Sites[i].Name)
	}
	searched := 1
	if w.Site == "" {
		searched = len(s.discover.Sites())
	}
	v.Empty = len(res.Hits) == 0 && len(res.Unavailable) < searched
	for _, h := range res.Hits {
		hit := ui.DiscoverHit{Site: h.Site, URL: h.URL, Title: h.Title, Meta: hitMeta(ctx, h)}
		if h.Photo != "" {
			hit.Photo = ui.DiscoverPhotoHref(h.Photo)
		}
		v.Hits = append(v.Hits, hit)
	}
	if err := s.markHits(ctx, sc, user, v.Hits); err != nil {
		return v, err
	}
	return v, nil
}

// webLoading is what the section shows while webView(w) fetches: the
// pages already cached with a placeholder below them after "More
// results", else placeholders alone.
func (s *Server) webLoading(ctx context.Context, sc store.Scoped, user uuid.UUID, w ui.WebSearch) (ui.WebView, error) {
	if prev := w; w.Pages > 1 {
		prev.Pages--
		if s.discover.Cached(prev.Query, prev.Site, prev.Pages) {
			v, err := s.webView(ctx, sc, user, prev)
			v.LoadingMore = true
			return v, err
		}
	}
	v := s.webBase(w)
	v.Loading = true
	return v, nil
}

// markHits sets each hit's state: in the library, or the viewer's import
// of it.
func (s *Server) markHits(ctx context.Context, sc store.Scoped, user uuid.UUID, hits []ui.DiscoverHit) error {
	urls := make([]string, len(hits))
	for i, h := range hits {
		urls[i] = h.URL
	}
	inLibrary, err := s.recipes.BySourceURL(ctx, sc, urls)
	if err != nil {
		return err
	}
	jobs, err := s.ai.Jobs(ctx, sc, user)
	if err != nil {
		return err
	}
	// Jobs come newest first; the newest import of a link counts.
	imports := map[string]ai.Job{}
	for _, j := range jobs {
		if _, seen := imports[j.SourceURL]; !seen && j.SourceURL != "" && j.Status != ai.JobCanceled {
			imports[j.SourceURL] = j
		}
	}
	for i := range hits {
		hit := &hits[i]
		job, imported := imports[hit.URL]
		slug, saved := inLibrary[hit.URL]
		hit.State, hit.Href = ui.DiscoverNew, ""
		switch {
		// A draft waiting for review or an import under way comes first:
		// it needs the viewer, even for a recipe saved before.
		case imported && job.Status != ai.JobFailed:
			hit.State, hit.Href = ui.DiscoverRunning, ui.ImportHref(job.ID.String())
			if job.Status == ai.JobReady {
				hit.State = ui.DiscoverReady
			}
		case saved:
			hit.State, hit.Href = ui.DiscoverLibrary, ui.RecipeHref(slug)
		case imported:
			hit.State, hit.Href = ui.DiscoverFailed, ui.ImportHref(job.ID.String())
		}
	}
	return nil
}

// hitMeta is a hit's rating and difficulty line.
func hitMeta(ctx context.Context, h discover.Hit) string {
	var parts []string
	switch {
	case h.Stars == 0:
	case h.Ratings == 1:
		parts = append(parts, i18n.T(ctx, "discover.rating.one", h.Stars))
	default:
		parts = append(parts, i18n.T(ctx, "discover.rating.many", h.Stars, h.Ratings))
	}
	if h.Level != "" {
		parts = append(parts, h.Level)
	}
	return strings.Join(parts, " · ")
}

// discoverImport starts importing a hit and shows the search again, the
// hit now marked as importing. An import from the preview answers with the
// preview.
func (s *Server) discoverImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	id := identity(r)
	ws := s.webSearch(r.PostFormValue("q"), r.PostFormValue("site"), r.PostFormValue("wp"))
	// A site's recipe is imported in the form its hits use, so the hit
	// follows the import whatever spelling of the address was posted.
	link := r.PostFormValue("url")
	recipe, _, isRecipe := s.discover.RecipeURL(link)
	if isRecipe {
		link = recipe
	}
	var alert string
	_, err = s.ai.StartImport(ctx, sc, id.UserID, ai.ImportInput{URL: link})
	var fe fieldErrors
	switch {
	case err == nil:
	case errors.Is(err, ai.ErrTooManyJobs):
		alert = i18n.T(ctx, "import.error.tooMany", ai.MaxActiveJobs)
	case errors.As(err, &fe):
		for _, key := range fe.Fields() {
			alert = i18n.T(ctx, key)
		}
	default:
		s.serverError(w, r, err)
		return
	}
	if isRecipe && r.PostFormValue("from") == "preview" {
		s.answerPreviewImport(w, r, sc, id.UserID, link, ws, alert)
		return
	}
	if alert == "" && !isDatastar(r) {
		seeOther(w, r, ui.SearchHref(ws))
		return
	}
	if isDatastar(r) {
		v, err := s.webView(ctx, sc, id.UserID, ws)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		v.Alert = alert
		s.sendPatches(w, r, patchTempl(ui.SearchWeb(v)))
		return
	}
	v, err := s.searchView(ctx, sc, id, ws.Query)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Web, err = s.webView(ctx, sc, id.UserID, ws); err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Web.Alert = alert
	s.renderSearch(w, r, http.StatusUnprocessableEntity, v)
}

// discoverPhoto serves a hit's photo from the site.
func (s *Server) discoverPhoto(w http.ResponseWriter, r *http.Request) {
	res, err := s.discover.Photo(r.Context(), r.URL.Query().Get("u"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", res.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(res.Body)
}

// searchWebLive re-renders the search page's "From the web" section for
// the $wq, $site and $wpages signals, as the viewer's imports progress and
// recipes are saved. Datastar reopens it when the signals change. A search
// that is not cached shows placeholders while the sites are asked.
func (s *Server) searchWebLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	var sig struct {
		Query string `json:"wq"`
		Site  string `json:"site"`
		Pages int    `json:"wpages"`
	}
	if isDatastar(r) {
		if err := datastar.ReadSignals(r, &sig); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}
	ws := s.webSearch(sig.Query, sig.Site, strconv.Itoa(sig.Pages))
	s.serveLive(w, r, id, live.Stream{
		Refresh: func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			if !s.discover.Cached(ws.Query, ws.Site, ws.Pages) {
				v, err := s.webLoading(ctx, sc, id.UserID, ws)
				if err != nil {
					return err
				}
				if err := sse.PatchElementTempl(ui.SearchWeb(v)); err != nil {
					return err
				}
			}
			v, err := s.webView(ctx, sc, id.UserID, ws)
			if err != nil {
				return err
			}
			return sse.PatchElementTempl(ui.SearchWeb(v))
		},
		Relevant: discoverRelevant(id),
	})
}

// ---- Preview ----

// discoverPreview shows a recipe on the site as importing it would start
// it. Only the site's own recipe pages are previewed.
func (s *Server) discoverPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	link, site, ok := s.discover.RecipeURL(query.Get("u"))
	if !ok {
		s.notFound(w, r)
		return
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ws := s.webSearch(query.Get("q"), query.Get("site"), query.Get("wp"))
	v, err := s.discoverPreviewView(ctx, sc, identity(r), link, site, ws)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderDiscoverPreview(w, r, http.StatusOK, v)
}

// discoverPreviewView reads the recipe at link the way an import starts
// (its recipe data, matched to the household's items, without a model)
// and marks the viewer's import state.
func (s *Server) discoverPreviewView(ctx context.Context, sc store.Scoped, id *auth.Identity, link, site string, ws ui.WebSearch) (ui.DiscoverPreviewView, error) {
	v := ui.DiscoverPreviewView{Site: site, Search: ws, Hit: ui.DiscoverHit{Site: site, URL: link}}
	_, v.SourceHost = sourceLink(link)
	d, err := s.ai.PreviewDraft(ctx, sc, link, i18n.Locale(ctx))
	switch {
	case err == nil:
		rv := s.previewRecipe(ctx, d, link, unitSystem(id))
		v.Recipe, v.Hit.Title = &rv, rv.Name
	case errors.Is(err, ai.ErrNoRecipe):
		v.NoRecipe = true
	case errors.Is(err, ai.ErrUnreachable):
		s.log.WarnContext(ctx, "recipe preview", slog.String("url", link), slog.Any("err", err))
	default:
		return v, err
	}
	if err := s.markHit(ctx, sc, id.UserID, &v.Hit); err != nil {
		return v, err
	}
	return v, nil
}

func (s *Server) markHit(ctx context.Context, sc store.Scoped, user uuid.UUID, hit *ui.DiscoverHit) error {
	hits := []ui.DiscoverHit{*hit}
	if err := s.markHits(ctx, sc, user, hits); err != nil {
		return err
	}
	*hit = hits[0]
	return nil
}

// previewRecipe shows a draft the way the recipe page would once it is
// saved: amounts at the recipe's servings in the viewer's units, and
// ingredient mentions in the steps.
func (s *Server) previewRecipe(ctx context.Context, d ai.Draft, link string, sys groceries.System) ui.ReadOnlyRecipeView {
	rec := recipes.Recipe{Name: d.Name, Description: d.Description, Servings: d.Servings}
	if rec.Servings == 0 {
		rec.Servings = ai.DefaultServings
	}
	for _, ing := range d.Ingredients {
		rec.Ingredients = append(rec.Ingredients, recipes.Ingredient{ItemName: ing.ItemName, Amount: ing.Amount, Icon: ing.Icon})
	}
	for _, st := range d.Steps {
		step := recipes.Step{Text: st.Text}
		if st.TimerSeconds > 0 {
			t := st.TimerSeconds
			step.TimerSeconds = &t
		}
		rec.Steps = append(rec.Steps, step)
	}
	v := ui.ReadOnlyRecipeView{Tint: ui.Tint(link), Name: d.Name, Description: d.Description, Servings: rec.Servings}
	if d.PrepMin > 0 {
		v.Prep = minutes(ctx, &d.PrepMin)
	}
	if d.CookMin > 0 {
		v.Cook = minutes(ctx, &d.CookMin)
	}
	if s.discover.IsPhoto(d.ImageURL) {
		v.PhotoURL = ui.DiscoverPhotoHref(d.ImageURL)
	}
	v.SourceURL, v.SourceHost = sourceLink(link)
	amounts := scaledAmounts(ctx, rec, rec.Servings, sys)
	for i, ing := range rec.Ingredients {
		v.Ingredients = append(v.Ingredients, ui.IngredientView{Name: ing.ItemName, Amount: amounts[i]})
	}
	v.Steps = stepParts(rec, amounts, sys, firstInMethod)
	return v
}

func (s *Server) renderDiscoverPreview(w http.ResponseWriter, r *http.Request, status int, v ui.DiscoverPreviewView) {
	ctx := r.Context()
	title := i18n.T(ctx, "discover.preview.title")
	if v.Recipe != nil {
		title = v.Recipe.Name
	}
	p := s.page(r, title, ui.NavRecipes)
	p.BackHref, p.BackLabel = ui.SearchHref(v.Search), i18n.T(ctx, "discover.back")
	if v.Hit.State == ui.DiscoverRunning {
		p.Refresh = discoverRefresh
	}
	s.render(w, r, status, ui.DiscoverPreview(p, v))
}

// answerPreviewImport answers an import started from the preview: the
// import state in place with JavaScript, else the preview again (with the
// error when it failed).
func (s *Server) answerPreviewImport(w http.ResponseWriter, r *http.Request, sc store.Scoped, user uuid.UUID, link string, ws ui.WebSearch, alert string) {
	ctx := r.Context()
	if alert == "" && !isDatastar(r) {
		seeOther(w, r, ui.DiscoverPreviewHref(link, ws))
		return
	}
	if isDatastar(r) {
		v := ui.DiscoverPreviewView{Search: ws, Hit: ui.DiscoverHit{URL: link}, Alert: alert}
		if err := s.markHit(ctx, sc, user, &v.Hit); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.sendPatches(w, r, patchTempl(ui.DiscoverPreviewAction(v)))
		return
	}
	_, site, _ := s.discover.RecipeURL(link)
	v, err := s.discoverPreviewView(ctx, sc, identity(r), link, site, ws)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v.Alert = alert
	s.renderDiscoverPreview(w, r, http.StatusUnprocessableEntity, v)
}

// discoverPreviewLive re-renders the preview's import state as the
// viewer's import progresses and recipes are saved. It does not read the
// recipe page again.
func (s *Server) discoverPreviewLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	query := r.URL.Query()
	link, _, ok := s.discover.RecipeURL(query.Get("u"))
	if !ok {
		s.notFound(w, r)
		return
	}
	v := ui.DiscoverPreviewView{Search: s.webSearch(query.Get("q"), query.Get("site"), query.Get("wp")), Hit: ui.DiscoverHit{URL: link}}
	s.serveLive(w, r, id, live.Stream{
		Refresh: func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			if err := s.markHit(ctx, sc, id.UserID, &v.Hit); err != nil {
				return err
			}
			return sse.PatchElementTempl(ui.DiscoverPreviewAction(v))
		},
		Relevant: discoverRelevant(id),
	})
}

// discoverRelevant picks the events that change a hit's state.
func discoverRelevant(id *auth.Identity) func(live.Event) bool {
	return func(e live.Event) bool {
		switch e.Kind {
		case live.ImportChanged:
			return e.UserID == id.UserID
		case live.RecipesChanged, live.RecipeChanged:
			return true
		}
		return false
	}
}
