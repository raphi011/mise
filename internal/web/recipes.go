package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// quickMinutes is the "Under 30 min" quick filter.
const quickMinutes = 30

var sortKeys = map[recipes.Sort]i18n.Key{
	recipes.SortRecentlyCooked: "recipes.sort.cooked",
	recipes.SortRecentlyAdded:  "recipes.sort.added",
	recipes.SortMostCooked:     "recipes.sort.most",
	recipes.SortName:           "recipes.sort.name",
	recipes.SortQuickest:       "recipes.sort.quickest",
}

// parseFilter reads the library filter from a query. Unknown or malformed
// values are ignored, so a stale link still shows the library.
func parseFilter(q url.Values) recipes.Filter {
	var f recipes.Filter
	f.AddedBy = parseIDs(q["by"])
	f.Tags = parseIDs(q["tag"])
	if m, err := strconv.Atoi(q.Get("max")); err == nil && m > 0 && m <= 24*60 {
		f.MaxMinutes = m
	}
	f.Favourites = q.Get("fav") == "1"
	f.NotCookedRecently = q.Get("stale") == "1"
	f.NeverCooked = q.Get("never") == "1"
	f.Sort = recipes.SortRecentlyCooked
	if s := recipes.Sort(q.Get("sort")); slices.Contains(recipes.Sorts, s) {
		f.Sort = s
	}
	return f
}

func parseIDs(vals []string) []uuid.UUID {
	var out []uuid.UUID
	for _, v := range vals {
		if id, err := uuid.Parse(v); err == nil && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// encodeFilter is the query of a filter, without defaults.
func encodeFilter(f recipes.Filter) string {
	q := url.Values{}
	for _, id := range f.AddedBy {
		q.Add("by", id.String())
	}
	for _, id := range f.Tags {
		q.Add("tag", id.String())
	}
	if f.MaxMinutes > 0 {
		q.Set("max", strconv.Itoa(f.MaxMinutes))
	}
	if f.Favourites {
		q.Set("fav", "1")
	}
	if f.NotCookedRecently {
		q.Set("stale", "1")
	}
	if f.NeverCooked {
		q.Set("never", "1")
	}
	if f.Sort != "" && f.Sort != recipes.SortRecentlyCooked {
		q.Set("sort", string(f.Sort))
	}
	return q.Encode()
}

func libraryHref(f recipes.Filter) string {
	if q := encodeFilter(f); q != "" {
		return "/recipes?" + q
	}
	return "/recipes"
}

// toggleID returns ids with id added, or removed when present.
func toggleID(ids []uuid.UUID, id uuid.UUID) []uuid.UUID {
	if i := slices.Index(ids, id); i >= 0 {
		return slices.Delete(slices.Clone(ids), i, i+1)
	}
	return append(slices.Clone(ids), id)
}

// minutes formats a duration in minutes.
func minutes(ctx context.Context, m *int) string {
	if m == nil {
		return ""
	}
	return i18n.T(ctx, "recipes.minutes", *m)
}

func servingsText(ctx context.Context, n int) string {
	if n == 1 {
		return i18n.T(ctx, "recipes.servingsOne")
	}
	return i18n.T(ctx, "recipes.servings", n)
}

// cardMeta is a card's second line: total time and first tag, or the
// servings when the time is unknown.
func cardMeta(ctx context.Context, c recipes.Card) string {
	var parts []string
	if c.TotalMin != nil {
		parts = append(parts, minutes(ctx, c.TotalMin))
	} else {
		parts = append(parts, servingsText(ctx, c.Servings))
	}
	if len(c.Tags) > 0 {
		parts = append(parts, c.Tags[0])
	}
	return strings.Join(parts, " · ")
}

// personLabel is "you" for the viewer, else the member's name.
func personLabel(ctx context.Context, p recipes.Person, viewer uuid.UUID) string {
	if p.ID == viewer {
		return i18n.T(ctx, "recipes.you")
	}
	return p.Name
}

func (s *Server) cardOf(ctx context.Context, c recipes.Card, viewer uuid.UUID) ui.RecipeCard {
	return ui.RecipeCard{
		ID: c.ID.String(), Name: c.Name, Photo: c.Photo, Meta: cardMeta(ctx, c), Favourite: c.Favourite,
		By: ui.Viewer{Name: c.CreatedBy.Name, Color: c.CreatedBy.Color}, ByLabel: personLabel(ctx, c.CreatedBy, viewer),
	}
}

// libraryView loads the Recipes page region for the viewer and filter.
func (s *Server) libraryView(ctx context.Context, sc store.Scoped, id *auth.Identity, f recipes.Filter) (ui.LibraryView, error) {
	cards, err := s.recipes.Library(ctx, sc, id.UserID, f)
	if err != nil {
		return ui.LibraryView{}, err
	}
	total, err := s.recipes.Count(ctx, sc)
	if err != nil {
		return ui.LibraryView{}, err
	}
	tags, err := s.recipes.Tags(ctx, sc)
	if err != nil {
		return ui.LibraryView{}, err
	}
	members, err := s.auth.Members(ctx, sc)
	if err != nil {
		return ui.LibraryView{}, err
	}
	v := ui.LibraryView{Query: encodeFilter(f), Total: total, Filtered: !f.IsZero(), Sort: i18n.T(ctx, sortKeys[f.Sort])}
	if !v.Filtered {
		if v.Tonight, err = s.tonightCard(ctx, sc); err != nil {
			return ui.LibraryView{}, err
		}
	}
	for _, c := range cards {
		v.Cards = append(v.Cards, s.cardOf(ctx, c, id.UserID))
	}

	mine := len(f.AddedBy) == 1 && f.AddedBy[0] == id.UserID
	mineF := f
	mineF.AddedBy = nil
	if !mine {
		mineF.AddedBy = []uuid.UUID{id.UserID}
	}
	favF := f
	favF.Favourites = !f.Favourites
	quickF := f
	quickF.MaxMinutes = 0
	if f.MaxMinutes != quickMinutes {
		quickF.MaxMinutes = quickMinutes
	}
	v.Chips = []ui.FilterChip{
		{Label: i18n.T(ctx, "recipes.mine"), Href: libraryHref(mineF), On: mine},
		{Label: i18n.T(ctx, "recipes.favourites"), Href: libraryHref(favF), On: f.Favourites},
		{Label: i18n.T(ctx, "recipes.quick", quickMinutes), Href: libraryHref(quickF), On: f.MaxMinutes == quickMinutes},
	}
	for _, t := range tags {
		tf := f
		tf.Tags = toggleID(f.Tags, t.ID)
		chip := ui.FilterChip{Label: t.Name, Href: libraryHref(tf), On: slices.Contains(f.Tags, t.ID), Count: t.Count}
		if t.Pinned {
			v.Chips = append(v.Chips, chip)
		}
		v.Tags = append(v.Tags, chip)
	}
	for _, m := range members {
		pf := f
		pf.AddedBy = toggleID(f.AddedBy, m.UserID)
		label := m.Name
		if m.UserID == id.UserID {
			label = i18n.T(ctx, "recipes.youCap")
		}
		v.People = append(v.People, ui.FilterChip{
			Label: label, Href: libraryHref(pf), On: slices.Contains(f.AddedBy, m.UserID),
			Color: m.AvatarColor, Initial: ui.Initial(m.Name), Count: -1,
		})
	}
	return v, nil
}

func (s *Server) libraryPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.libraryView(ctx, sc, identity(r), parseFilter(r.URL.Query()))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var undo *ui.Toast
	if deleted, err := uuid.Parse(r.URL.Query().Get("deleted")); err == nil {
		undo = &ui.Toast{
			Key:        strconv.FormatInt(time.Now().UnixNano(), 36),
			Message:    i18n.T(ctx, "recipes.toast.deleted"),
			UndoAction: "/recipes/" + deleted.String() + "/restore",
		}
	}
	p := s.page(r, i18n.T(ctx, "page.recipes.title"), ui.NavRecipes)
	p.Actions = ui.LibraryActions()
	p.NavExtra = ui.NavTags(v)
	s.render(w, r, http.StatusOK, ui.Library(p, v, undo))
}

// ---- Filter sheet ----

func (s *Server) filterView(ctx context.Context, sc store.Scoped, id *auth.Identity, f recipes.Filter) (ui.FilterSheetView, error) {
	tags, err := s.recipes.Tags(ctx, sc)
	if err != nil {
		return ui.FilterSheetView{}, err
	}
	members, err := s.auth.Members(ctx, sc)
	if err != nil {
		return ui.FilterSheetView{}, err
	}
	cards, err := s.recipes.Library(ctx, sc, id.UserID, f)
	if err != nil {
		return ui.FilterSheetView{}, err
	}
	v := ui.FilterSheetView{
		Count: len(cards),
		Query: encodeFilter(f), Favourites: f.Favourites, Stale: f.NotCookedRecently, Never: f.NeverCooked, Sort: string(f.Sort),
	}
	if f.MaxMinutes > 0 {
		v.Max = strconv.Itoa(f.MaxMinutes)
	}
	for _, m := range members {
		label := m.Name
		if m.UserID == id.UserID {
			label = i18n.T(ctx, "recipes.youCap")
		}
		v.People = append(v.People, ui.FilterOption{Value: m.UserID.String(), Label: label, Initial: ui.Initial(m.Name), Color: m.AvatarColor, Checked: slices.Contains(f.AddedBy, m.UserID)})
	}
	for _, t := range tags {
		v.Tags = append(v.Tags, ui.FilterOption{Value: t.ID.String(), Label: t.Name, Checked: slices.Contains(f.Tags, t.ID)})
	}
	for _, so := range recipes.Sorts {
		v.Sorts = append(v.Sorts, ui.Option{Value: string(so), Label: i18n.T(ctx, sortKeys[so])})
	}
	return v, nil
}

func (s *Server) filterPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.filterView(ctx, sc, identity(r), parseFilter(r.URL.Query()))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.FilterSheet(v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "recipes.filter.title"), ui.NavRecipes)
	p.BackHref, p.BackLabel = libraryHref(parseFilter(r.URL.Query())), i18n.T(ctx, "recipes.back")
	s.render(w, r, http.StatusOK, ui.FilterPage(p, v))
}

// filterCount patches the filter sheet's submit button with how many
// recipes the posted choices show.
func (s *Server) filterCount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards, err := s.recipes.Library(ctx, sc, identity(r).UserID, parseFilter(r.URL.Query()))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.FilterSubmit(len(cards))))
}

// ---- Search ----

// split cuts text at a match span. A span that is not a valid cut (empty,
// out of range, inside a rune) marks nothing.
func split(text string, sp recipes.Span) (pre, hit, post string) {
	if sp.Start < 0 || sp.End > len(text) || sp.Start >= sp.End ||
		!utf8.RuneStart(byteAt(text, sp.Start)) || (sp.End < len(text) && !utf8.RuneStart(text[sp.End])) {
		return text, "", ""
	}
	return text[:sp.Start], text[sp.Start:sp.End], text[sp.End:]
}

func byteAt(s string, i int) byte {
	if i >= len(s) {
		return 0
	}
	return s[i]
}

func (s *Server) searchView(ctx context.Context, sc store.Scoped, id *auth.Identity, q string) (ui.SearchView, error) {
	q = strings.TrimSpace(q)
	v := ui.SearchView{Query: q}
	total, err := s.recipes.Count(ctx, sc)
	if err != nil {
		return ui.SearchView{}, err
	}
	v.Total = total
	if q == "" {
		return v, nil
	}
	res, err := s.recipes.Search(ctx, sc, id.UserID, q)
	if err != nil {
		return ui.SearchView{}, err
	}
	lang := i18n.Locale(ctx)
	for _, m := range res.Names {
		h := ui.SearchHit{ID: m.ID.String(), Name: m.Name, Photo: m.Photo, Meta: cardMeta(ctx, m.Card)}
		h.Pre, h.Hit, h.Post = split(m.Name, m.Span)
		v.Names = append(v.Names, h)
	}
	for _, m := range res.Ingredients {
		h := ui.SearchHit{ID: m.ID.String(), Name: m.Name, Photo: m.Photo, Amount: recipes.DisplayAmount(m.Amount, lang)}
		h.Pre, h.Hit, h.Post = split(m.ItemName, m.Span)
		v.Ingredients = append(v.Ingredients, h)
	}
	v.Status = i18n.T(ctx, "recipes.search.status", len(v.Names), len(v.Ingredients))
	return v, nil
}

func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.searchView(ctx, sc, identity(r), r.URL.Query().Get("q"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := s.page(r, i18n.T(ctx, "recipes.search.title"), ui.NavRecipes)
	p.BackHref, p.BackLabel = "/recipes", i18n.T(ctx, "recipes.back")
	s.render(w, r, http.StatusOK, ui.Search(p, v))
}

// searchResults answers the search field as the user types.
func (s *Server) searchResults(w http.ResponseWriter, r *http.Request) {
	if !isDatastar(r) {
		http.Redirect(w, r, "/recipes/search?"+url.Values{"q": {r.URL.Query().Get("q")}}.Encode(), http.StatusFound)
		return
	}
	var sig struct {
		Q string `json:"q"`
	}
	if err := datastar.ReadSignals(r, &sig); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v, err := s.searchView(ctx, sc, identity(r), sig.Q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.SearchResults(v)))
}

// ---- Images ----

// image serves a stored photo of the viewer's household. Content hashes
// never change, so browsers may keep it for a year; it is private because
// it needs the session.
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	f, err := s.images.Open(identity(r).HouseholdID, r.PathValue("hash"), images.Size(r.PathValue("size")))
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
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// ---- Live ----

// libraryLive re-renders the Recipes page for the viewer's filter when
// recipes, tags or the plan change.
func (s *Server) libraryLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	f := parseFilter(r.URL.Query())
	s.serveLive(w, r, id, live.Stream{
		Refresh: func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			v, err := s.libraryView(ctx, sc, id, f)
			if err != nil {
				return err
			}
			if err := sse.PatchElementTempl(ui.LibraryRegion(v)); err != nil {
				return err
			}
			return sse.PatchElementTempl(ui.NavTags(v))
		},
		// The plan changes the "tonight" card.
		Relevant: func(e live.Event) bool { return e.Kind == live.RecipesChanged || e.Kind == live.PlanChanged },
	})
}
