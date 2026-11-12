package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/catalog"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/jobs"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// suggestLimit caps the suggestions under the add field.
const suggestLimit = 5

// isDatastar reports whether r is a Datastar action, which expects an SSE
// response with patches. Without it, forms post the classic way and get a
// redirect, so every page works without JavaScript.
func isDatastar(r *http.Request) bool { return r.Header.Get("Datastar-Request") != "" }

// groceryDefaults is what a household starts with, in the language lang
// (the household's default language).
func groceryDefaults(ctx context.Context, lang string) groceries.Defaults {
	ctx = i18n.WithLocale(ctx, i18n.Match(lang))
	return groceries.Defaults{
		ListName: i18n.T(ctx, "groceries.defaultList"),
		Categories: []groceries.NewCategory{
			{Name: i18n.T(ctx, "groceries.category.produce"), Color: "herb", CatalogKey: "produce"},
			{Name: i18n.T(ctx, "groceries.category.bakery"), Color: "sand", CatalogKey: "bakery"},
			{Name: i18n.T(ctx, "groceries.category.meatFish"), Color: "coral", CatalogKey: "meat_fish"},
			{Name: i18n.T(ctx, "groceries.category.fridge"), Color: "sky", CatalogKey: "fridge"},
			{Name: i18n.T(ctx, "groceries.category.frozen"), Color: "plum", CatalogKey: "frozen"},
			{Name: i18n.T(ctx, "groceries.category.pantry"), Color: "amber", CatalogKey: "pantry"},
			{Name: i18n.T(ctx, "groceries.category.spices"), Color: "herb", CatalogKey: "spices"},
			{Name: i18n.T(ctx, "groceries.category.drinks"), Color: "sky", CatalogKey: "drinks"},
			{Name: i18n.T(ctx, "groceries.category.household"), Color: "coral", CatalogKey: "household"},
		},
	}
}

// groceriesView loads the list page region for the viewer. A list that is
// gone renders as such, with the remaining lists as tabs.
func (s *Server) groceriesView(ctx context.Context, sc store.Scoped, id *auth.Identity, listID uuid.UUID) (ui.GroceriesView, error) {
	lists, err := s.groceries.Lists(ctx, sc)
	if err != nil {
		return ui.GroceriesView{}, err
	}
	v := ui.GroceriesView{List: ui.ListTab{ID: listID.String()}}
	for _, l := range lists {
		tab := ui.ListTab{ID: l.ID.String(), Name: l.Name, Open: l.Open, Current: l.ID == listID}
		v.Lists = append(v.Lists, tab)
		if tab.Current {
			v.List = tab
		}
	}
	gv, err := s.groceries.ListView(ctx, sc, listID)
	if errors.Is(err, groceries.ErrNotFound) {
		v.Gone = true
		return v, nil
	}
	if err != nil {
		return ui.GroceriesView{}, err
	}
	v.Open = gv.List.Open
	v.InCart = len(gv.Cart)
	for _, g := range gv.Groups {
		group := ui.EntryGroup{Name: i18n.T(ctx, "groceries.uncategorised")}
		if g.Category != nil {
			group.Name, group.Color = g.Category.Name, g.Category.Color
		}
		for _, e := range g.Entries {
			group.Entries = append(group.Entries, entryRow(ctx, e, id))
		}
		v.Groups = append(v.Groups, group)
	}
	for _, e := range gv.Cart {
		v.Cart = append(v.Cart, entryRow(ctx, e, id))
	}
	for _, it := range gv.Usual {
		v.Usual = append(v.Usual, ui.ItemChip{ID: it.ID.String(), Name: it.Name})
	}
	return v, nil
}

// entryRow shows an entry to the viewer id, with its amount in the viewer's
// unit system.
func entryRow(ctx context.Context, e groceries.Entry, id *auth.Identity) ui.EntryRow {
	amount := groceries.Convert(e.Amount, unitSystem(id), i18n.Locale(ctx)).Text
	row := ui.EntryRow{ID: e.ID.String(), Name: e.ItemName, Icon: e.ItemIcon, Amount: amount, Note: e.Note, Recipe: e.RecipeName}
	if e.Category != nil {
		row.Color = e.Category.Color
	}
	if e.AddedBy != id.UserID {
		row.By = &ui.Viewer{Name: e.AddedByName, Color: e.AddedByColor}
	}
	return row
}

// groceriesHome shows the household's first list. Setup created it (and
// SeedGroceries backfilled older households), so the GET stays read-only.
func (s *Server) groceriesHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	l, err := s.groceries.FirstList(ctx, sc)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderList(w, r, http.StatusOK, sc, l.ID, ui.Form{})
}

func (s *Server) listPage(w http.ResponseWriter, r *http.Request) {
	listID, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	s.renderList(w, r, http.StatusOK, sc, listID, ui.Form{})
}

func (s *Server) renderList(w http.ResponseWriter, r *http.Request, status int, sc store.Scoped, listID uuid.UUID, add ui.Form) {
	ctx := r.Context()
	v, err := s.groceriesView(ctx, sc, identity(r), listID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if v.Gone {
		s.notFound(w, r)
		return
	}
	v.AddForm = add
	s.render(w, r, status, ui.Groceries(s.page(r, i18n.T(ctx, "page.groceries.title"), ui.NavGroceries), v))
}

// groceriesTarget parses the {id} path value and the scope.
func (s *Server) groceriesTarget(w http.ResponseWriter, r *http.Request) (uuid.UUID, store.Scoped, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return uuid.Nil, store.Scoped{}, false
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return uuid.Nil, store.Scoped{}, false
	}
	return id, sc, true
}

// patch is one more thing a Datastar response sends after the list region.
type patch func(sse *datastar.ServerSentEventGenerator) error

func patchTempl(c templ.Component) patch {
	return func(sse *datastar.ServerSentEventGenerator) error { return sse.PatchElementTempl(c) }
}

func patchSignals(signals map[string]any) patch {
	return func(sse *datastar.ServerSentEventGenerator) error { return sse.MarshalAndPatchSignals(signals) }
}

// toast shows a message with an optional Undo.
func toast(msg, undo string, fields map[string]string) patch {
	key := strconv.FormatInt(time.Now().UnixNano(), 36)
	return patchTempl(ui.ToastRegion(ui.Toast{Key: key, Message: msg, UndoAction: undo, UndoFields: fields}))
}

// clearToast removes the toast, e.g. after its Undo.
var clearToast = patchTempl(ui.EmptyToast())

// respond answers a successful list action: Datastar gets the viewer's list
// region and the extra patches; a plain form post is redirected to the list.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, sc store.Scoped, listID uuid.UUID, extra ...patch) {
	if !isDatastar(r) {
		seeOther(w, r, "/groceries/lists/"+listID.String())
		return
	}
	ctx := r.Context()
	v, err := s.groceriesView(ctx, sc, identity(r), listID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sse := datastar.NewSSE(w, r)
	for _, p := range append([]patch{patchTempl(ui.GroceriesRegion(v))}, extra...) {
		if err := p(sse); err != nil {
			s.log.WarnContext(ctx, "send patch", slog.String("route", r.Pattern), slog.Any("err", err))
			return
		}
	}
}

// sendPatches answers a Datastar action with patches only.
func (s *Server) sendPatches(w http.ResponseWriter, r *http.Request, patches ...patch) {
	sse := datastar.NewSSE(w, r)
	for _, p := range patches {
		if err := p(sse); err != nil {
			s.log.WarnContext(r.Context(), "send patch", slog.String("route", r.Pattern), slog.Any("err", err))
			return
		}
	}
}

// entryGone answers an action on an entry that no longer exists: Datastar
// clients get a note (their live stream already shows the current list).
func (s *Server) entryGone(w http.ResponseWriter, r *http.Request) {
	if !isDatastar(r) {
		s.notFound(w, r)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.EmptySheet()), toast(i18n.T(r.Context(), "groceries.toast.gone"), "", nil))
}

// ---- Add ----

func (s *Server) addEntry(w http.ResponseWriter, r *http.Request) {
	listID, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	lang := i18n.Locale(ctx)
	text := r.PostFormValue("text")
	// A clicked suggestion or chip names its item; Enter takes the
	// highlighted suggestion if it was computed for this text.
	pick := r.PostFormValue("pick")
	if pick == "" && r.PostFormValue("for") == text {
		pick = r.PostFormValue("sel")
	}
	amount, name := groceries.ParseEntry(text, lang)
	in := groceries.AddInput{ListID: listID, Name: name, Amount: amount, AddedBy: identity(r).UserID, Lang: lang}
	if key, ok := strings.CutPrefix(pick, "cat:"); ok {
		in.CatalogKey = key
	} else if pick != "" && pick != "new" {
		itemID, err := uuid.Parse(pick)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		in.ItemID = &itemID
	}
	_, err := s.groceries.Add(ctx, sc, in)
	if f, ok := withErrors(ctx, ui.Form{Values: map[string]string{"text": text}}, err); ok {
		if isDatastar(r) {
			s.sendPatches(w, r, patchTempl(ui.AddError(f.Error("text"))))
			return
		}
		s.renderList(w, r, http.StatusUnprocessableEntity, sc, listID, f)
		return
	}
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, listID,
		patchSignals(map[string]any{"add": "", "_opts": []string{}, "_sel": -1, "_for": ""}),
		patchTempl(ui.Suggestions(ui.SuggestView{})),
		patchTempl(ui.AddError("")),
	)
}

// suggest answers the add field as the user types: matching items, the
// highlighted one, and the option to create a new item.
func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	listID, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	if !isDatastar(r) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	var sig struct {
		Add string `json:"add"`
	}
	if err := datastar.ReadSignals(r, &sig); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	lang := i18n.Locale(ctx)
	amount, name := groceries.ParseEntry(sig.Add, lang)
	var v ui.SuggestView
	opts := []string{}
	sel := -1
	if name != "" {
		found, err := s.groceries.Suggest(ctx, sc, listID, name, suggestLimit)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		v.Amount = amount.Text
		exact := false
		for _, sg := range found {
			exact = exact || sg.Match == groceries.MatchExact
			o := suggestOption(ctx, sg, unitSystem(identity(r)))
			v.Options = append(v.Options, o)
			opts = append(opts, o.Pick)
		}
		if !exact {
			v.Create = groceries.CleanName(name)
			opts = append(opts, "new")
		}
		// Highlight the best match when it starts with what was typed;
		// otherwise Enter creates a new item.
		sel = len(opts) - 1
		if len(found) > 0 && found[0].Match <= groceries.MatchPrefix {
			sel = 0
		}
	}
	s.sendPatches(w, r,
		patchTempl(ui.Suggestions(v)),
		patchSignals(map[string]any{"_opts": opts, "_sel": sel, "_for": sig.Add}),
	)
}

// oftenBought is the purchase count from which a suggestion says "you buy
// these often".
const oftenBought = 3

func suggestOption(ctx context.Context, sg groceries.Suggestion, sys groceries.System) ui.SuggestOption {
	if sg.Catalog != nil {
		o := ui.SuggestOption{Pick: "cat:" + sg.Catalog.Key, Name: sg.Catalog.Name, Icon: sg.Catalog.Icon, Catalogue: true,
			Detail: i18n.T(ctx, "groceries.uncategorised")}
		if c := sg.Catalog.Category; c != nil {
			o.Detail, o.Color = c.Name, c.Color
		}
		return o
	}
	o := ui.SuggestOption{Pick: sg.Item.ID.String(), Name: sg.Item.Name, Icon: sg.Item.Icon, OnList: sg.OnList != nil}
	category := i18n.T(ctx, "groceries.uncategorised")
	if sg.Item.Category != nil {
		category, o.Color = sg.Item.Category.Name, sg.Item.Category.Color
	}
	switch {
	case sg.OnList != nil && sg.OnList.Text != "":
		o.Detail = i18n.T(ctx, "groceries.suggest.onListAmount", groceries.Convert(*sg.OnList, sys, i18n.Locale(ctx)).Text)
	case sg.OnList != nil:
		o.Detail = i18n.T(ctx, "groceries.suggest.onList")
	case sg.Item.PurchaseCount >= oftenBought:
		o.Detail = category + " · " + i18n.T(ctx, "groceries.suggest.often")
	default:
		o.Detail = category
	}
	return o
}

// ---- Check off ----

func (s *Server) checkEntry(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	e, err := s.groceries.Check(ctx, sc, id)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, e.ListID,
		toast(i18n.T(ctx, "groceries.toast.checked", e.ItemName), "/groceries/entries/"+e.ID.String()+"/uncheck", nil))
}

func (s *Server) uncheckEntry(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	e, err := s.groceries.Uncheck(r.Context(), sc, id)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, e.ListID, clearToast)
}

// ---- Remove ----

func (s *Server) removeEntry(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	e, err := s.groceries.Remove(ctx, sc, id)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, e.ListID, patchTempl(ui.EmptySheet()),
		toast(i18n.T(ctx, "groceries.toast.removed", e.ItemName), "/groceries/entries/"+e.ID.String()+"/restore", nil))
}

func (s *Server) restoreEntry(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	e, err := s.groceries.Restore(r.Context(), sc, id)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, e.ListID, clearToast)
}

// ---- Clear cart ----

func (s *Server) clearCartView(ctx context.Context, sc store.Scoped, listID uuid.UUID) (ui.ClearCartView, error) {
	gv, err := s.groceries.ListView(ctx, sc, listID)
	if err != nil {
		return ui.ClearCartView{}, err
	}
	v := ui.ClearCartView{ListID: listID.String(), ListName: gv.List.Name}
	for _, e := range gv.Cart {
		v.Items = append(v.Items, e.ItemName)
	}
	return v, nil
}

func (s *Server) clearCartPage(w http.ResponseWriter, r *http.Request) {
	listID, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := s.clearCartView(ctx, sc, listID)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.ClearCartDialog(v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "groceries.clear.title"), ui.NavGroceries)
	p.BackHref, p.BackLabel = "/groceries/lists/"+v.ListID, i18n.T(ctx, "groceries.backToList")
	s.render(w, r, http.StatusOK, ui.ClearCartPage(p, v))
}

func (s *Server) clearCart(w http.ResponseWriter, r *http.Request) {
	listID, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	cleared, err := s.groceries.ClearCart(ctx, sc, listID)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The install hint takes the confirm dialog's place; app.js decides
	// whether this device sees it.
	extra := []patch{patchTempl(ui.EmptySheet())}
	if cleared.Count > 0 {
		extra = []patch{patchTempl(ui.InstallHint()), toast(i18n.T(ctx, "groceries.toast.cleared"),
			"/groceries/lists/"+listID.String()+"/restore-cart", map[string]string{"at": cleared.At.Format(time.RFC3339Nano)})}
	}
	s.respond(w, r, sc, listID, extra...)
}

func (s *Server) restoreCart(w http.ResponseWriter, r *http.Request) {
	listID, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, r.PostFormValue("at"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if _, err := s.groceries.RestoreCart(r.Context(), sc, listID, at); errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, listID, clearToast)
}

// ---- Edit sheet ----

func (s *Server) editView(ctx context.Context, sc store.Scoped, e groceries.Entry, f ui.Form) (ui.EntryEditView, error) {
	cats, err := s.groceries.Categories(ctx, sc)
	if err != nil {
		return ui.EntryEditView{}, err
	}
	lists, err := s.groceries.Lists(ctx, sc)
	if err != nil {
		return ui.EntryEditView{}, err
	}
	v := ui.EntryEditView{ID: e.ID.String(), ItemName: e.ItemName, ItemIcon: e.ItemIcon, ListID: e.ListID.String(), Form: f}
	for _, k := range catalog.IconKeys() {
		v.Icons = append(v.Icons, ui.IconOption{Key: k, Label: i18n.T(ctx, i18n.Key("icon."+k))})
	}
	icon, err := s.groceries.ItemIcon(ctx, sc, e.ItemID)
	if err != nil {
		return ui.EntryEditView{}, err
	}
	if e.Category != nil {
		v.Color = e.Category.Color
	}
	for _, c := range cats {
		v.Categories = append(v.Categories, ui.Option{Value: c.ID.String(), Label: c.Name})
	}
	v.Categories = append(v.Categories, ui.Option{Value: "", Label: i18n.T(ctx, "groceries.uncategorised")})
	for _, l := range lists {
		v.Lists = append(v.Lists, ui.Option{Value: l.ID.String(), Label: l.Name})
	}
	if v.Form.Values == nil {
		category := ""
		if e.Category != nil {
			category = e.Category.ID.String()
		}
		v.Form.Values = map[string]string{
			"name": e.ItemName, "amount": e.Amount.Text, "note": e.Note, "category": category, "list": e.ListID.String(), "icon": icon,
		}
	}
	return v, nil
}

func (s *Server) editEntryPage(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	e, err := s.groceries.Entry(ctx, sc, id)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderEdit(w, r, http.StatusOK, sc, e, ui.Form{})
}

func (s *Server) renderEdit(w http.ResponseWriter, r *http.Request, status int, sc store.Scoped, e groceries.Entry, f ui.Form) {
	ctx := r.Context()
	v, err := s.editView(ctx, sc, e, f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isDatastar(r) {
		s.sendPatches(w, r, patchTempl(ui.EntryEditSheet(v)))
		return
	}
	p := s.page(r, i18n.T(ctx, "groceries.edit.title", e.ItemName), ui.NavGroceries)
	p.BackHref, p.BackLabel = "/groceries/lists/"+v.ListID, i18n.T(ctx, "groceries.backToList")
	s.render(w, r, status, ui.EntryEditPage(p, v))
}

func (s *Server) saveEntry(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	e, err := s.groceries.Entry(ctx, sc, id)
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r, "name", "amount", "category", "new_category", "list", "note", "icon")
	u := groceries.EntryUpdate{
		Name:        f.Value("name"),
		Amount:      groceries.ParseAmount(f.Value("amount"), i18n.Locale(ctx)),
		Note:        f.Value("note"),
		NewCategory: groceries.NewCategory{Name: f.Value("new_category")},
		ListID:      e.ListID,
		Icon:        f.Value("icon"),
	}
	if c := f.Value("category"); c != "" {
		catID, err := uuid.Parse(c)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		u.CategoryID = &catID
	}
	if l := f.Value("list"); l != "" {
		if u.ListID, err = uuid.Parse(l); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}
	err = s.groceries.UpdateEntry(ctx, sc, id, u)
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderEdit(w, r, http.StatusUnprocessableEntity, sc, e, f)
		return
	}
	if errors.Is(err, groceries.ErrNotFound) {
		s.entryGone(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.respond(w, r, sc, e.ListID, patchTempl(ui.EmptySheet()))
}

// ---- Live ----

// liveStream is a page's SSE stream; ?page= names the page.
func (s *Server) liveStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	id := identity(r)
	switch r.URL.Query().Get("page") {
	case "groceries":
		s.groceriesLive(w, r, sc, id)
	case "recipes":
		s.libraryLive(w, r, sc, id)
	case "recipe":
		s.recipeLive(w, r, sc, id)
	case "plan":
		s.planLive(w, r, sc, id)
	case "import":
		s.importLive(w, r, sc, id)
	default:
		s.notFound(w, r)
	}
}

// serveLive subscribes to the household's events and serves st until the
// stream ends. It fills in the session check and heartbeat.
func (s *Server) serveLive(w http.ResponseWriter, r *http.Request, id *auth.Identity, st live.Stream) {
	ctx := r.Context()
	sub := s.live.Subscribe(id.HouseholdID, id.UserID, id.SessionID)
	defer sub.Close()
	st.Alive = func(ctx context.Context) (bool, error) {
		return s.auth.SessionActive(ctx, id.SessionID)
	}
	st.Heartbeat = s.heartbeat
	if err := live.Serve(w, r, sub, st); err != nil && ctx.Err() == nil {
		s.log.ErrorContext(ctx, "live stream", slog.String("page", r.URL.Query().Get("page")), slog.Any("err", err))
	}
}

// groceriesLive re-renders the list region for this viewer whenever any
// list, the lists or the catalogue change.
func (s *Server) groceriesLive(w http.ResponseWriter, r *http.Request, sc store.Scoped, id *auth.Identity) {
	listID, err := uuid.Parse(r.URL.Query().Get("list"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.serveLive(w, r, id, live.Stream{
		Refresh: func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error {
			v, err := s.groceriesView(ctx, sc, id, listID)
			if err != nil {
				return err
			}
			return sse.PatchElementTempl(ui.GroceriesRegion(v))
		},
		// Every list event is relevant, not only the shown list's: the tabs
		// show each list's open count. Recipe edits are too: entries name
		// the recipe they come from.
		Relevant: func(e live.Event) bool {
			switch e.Kind {
			case live.ListChanged, live.ListsChanged, live.CatalogChanged, live.RecipeChanged:
				return true
			}
			return false
		},
	})
}

// CloseStreams ends every open live stream. Register it with
// http.Server.RegisterOnShutdown so graceful shutdown does not wait for them.
func (s *Server) CloseStreams() { s.live.Close() }

// SeedGroceries gives households created before Groceries existed their
// grocery defaults. Setup seeds new households itself. Call it once before
// serving.
func (s *Server) SeedGroceries(ctx context.Context) error {
	return jobs.SeedGroceries(ctx, s.db, s.groceries, func(lang string) groceries.Defaults {
		return groceryDefaults(ctx, lang)
	})
}

// LinkCatalog links households created before the catalogue to it.
func (s *Server) LinkCatalog(ctx context.Context) error {
	return jobs.LinkCatalog(ctx, s.db, s.groceries, func(lang string) groceries.Defaults {
		return groceryDefaults(ctx, lang)
	})
}

// seedHousehold creates a new household's grocery defaults inside the setup
// transaction.
func (s *Server) seedHousehold(ctx context.Context, q store.Querier, sc store.Scoped, lang string) error {
	return s.groceries.SeedTx(ctx, q, sc, groceryDefaults(ctx, lang))
}

// sessionsEnded tells the user's open streams to re-check their session.
func (s *Server) sessionsEnded(userID uuid.UUID) {
	s.live.PublishUser(userID, live.Event{Kind: live.SessionsChanged, UserID: userID})
}
