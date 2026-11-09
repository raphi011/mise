package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

// ---- Shopping lists (Settings › Shopping lists) ----

func (s *Server) createList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r, "name")
	_, err = s.groceries.CreateList(ctx, sc, f.Value("name"))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, settingsState{list: f})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/settings?done=listCreated#lists")
}

func (s *Server) renameList(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	f := formOf(r, "name")
	err := s.groceries.RenameList(ctx, sc, id, f.Value("name"))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, settingsState{list: f, listID: id.String()})
		return
	}
	if !s.groceriesError(w, r, err) {
		seeOther(w, r, "/settings?done=saved#lists")
	}
}

func (s *Server) moveList(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	to, err := strconv.Atoi(r.PostFormValue("to"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !s.groceriesError(w, r, s.groceries.MoveList(r.Context(), sc, id, to)) {
		seeOther(w, r, "/settings#lists")
	}
}

func (s *Server) deleteList(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	err := s.groceries.DeleteList(r.Context(), sc, id)
	if errors.Is(err, groceries.ErrLastList) {
		f := ui.Form{Alert: i18n.T(r.Context(), "groceries.error.lastList")}
		s.renderSettings(w, r, http.StatusConflict, settingsState{list: f, listID: id.String()})
		return
	}
	if !s.groceriesError(w, r, err) {
		seeOther(w, r, "/settings?done=listDeleted&undo="+id.String()+"#lists")
	}
}

func (s *Server) restoreList(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	if !s.groceriesError(w, r, s.groceries.RestoreList(r.Context(), sc, id)) {
		seeOther(w, r, "/settings?done=listRestored#lists")
	}
}

// groceriesError answers err from a settings action and reports whether it
// did; nil errors are left to the caller.
func (s *Server) groceriesError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, groceries.ErrNotFound):
		s.notFound(w, r)
	default:
		s.serverError(w, r, err)
	}
	return true
}

// ---- Categories (Settings › Categories & aisles) ----

type categoriesState struct {
	create ui.Form
	editID string
	edit   ui.Form
	notice string
}

func (s *Server) categoriesView(r *http.Request, st categoriesState) (ui.CategoriesView, error) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return ui.CategoriesView{}, err
	}
	cats, err := s.groceries.Categories(ctx, sc)
	if err != nil {
		return ui.CategoriesView{}, err
	}
	n, err := s.groceries.UncategorisedItems(ctx, sc)
	if err != nil {
		return ui.CategoriesView{}, err
	}
	v := ui.CategoriesView{Uncategorised: n, New: st.create, EditID: st.editID, Edit: st.edit, Notice: st.notice}
	for _, c := range cats {
		v.Categories = append(v.Categories, ui.CategoryRow{ID: c.ID.String(), Name: c.Name, Color: c.Color, Items: c.Items})
	}
	for _, c := range groceries.Colors {
		v.Colors = append(v.Colors, ui.Option{Value: c, Label: colorLabel(r, c)})
	}
	if v.New.Values == nil {
		// Suggest the next colour of the palette for a new category.
		v.New.Values = map[string]string{"color": groceries.Colors[len(cats)%len(groceries.Colors)]}
	}
	return v, nil
}

func (s *Server) categoriesPage(w http.ResponseWriter, r *http.Request) {
	s.renderCategories(w, r, http.StatusOK, categoriesState{notice: noticeFor(r)})
}

func (s *Server) renderCategories(w http.ResponseWriter, r *http.Request, status int, st categoriesState) {
	ctx := r.Context()
	v, err := s.categoriesView(r, st)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := s.page(r, i18n.T(ctx, "categories.title"), ui.NavSettings)
	p.BackHref, p.BackLabel = "/settings#groceries", i18n.T(ctx, "settings.back")
	s.render(w, r, status, ui.Categories(p, v))
}

func categoryInput(f ui.Form) groceries.NewCategory {
	return groceries.NewCategory{Name: f.Value("name"), Color: f.Value("color")}
}

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := formOf(r, "name", "color")
	_, err = s.groceries.CreateCategory(ctx, sc, categoryInput(f))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderCategories(w, r, http.StatusUnprocessableEntity, categoriesState{create: f})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/settings/categories?done=saved")
}

func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	f := formOf(r, "name", "color")
	err := s.groceries.UpdateCategory(ctx, sc, id, categoryInput(f))
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderCategories(w, r, http.StatusUnprocessableEntity, categoriesState{editID: id.String(), edit: f})
		return
	}
	if !s.groceriesError(w, r, err) {
		seeOther(w, r, "/settings/categories?done=saved")
	}
}

// moveCategory takes the target index from the arrow buttons' form, or
// from the JSON payload of a drop.
func (s *Server) moveCategory(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	var to int
	if isDatastar(r) {
		var payload struct {
			To *int `json:"to"`
		}
		if err := datastar.ReadSignals(r, &payload); err != nil || payload.To == nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		to = *payload.To
	} else {
		var err error
		if to, err = strconv.Atoi(r.PostFormValue("to")); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}
	if s.groceriesError(w, r, s.groceries.MoveCategory(r.Context(), sc, id, to)) {
		return
	}
	if !isDatastar(r) {
		seeOther(w, r, "/settings/categories")
		return
	}
	v, err := s.categoriesView(r, categoriesState{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sendPatches(w, r, patchTempl(ui.CategoriesBody(v)))
}

func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	id, sc, ok := s.groceriesTarget(w, r)
	if !ok {
		return
	}
	if !s.groceriesError(w, r, s.groceries.DeleteCategory(r.Context(), sc, id)) {
		seeOther(w, r, "/settings/categories?done=categoryDeleted")
	}
}

// undoListParam returns the list offered for restoring after a delete.
func undoListParam(r *http.Request) string {
	if r.URL.Query().Get("done") != "listDeleted" {
		return ""
	}
	id, err := uuid.Parse(r.URL.Query().Get("undo"))
	if err != nil {
		return ""
	}
	return id.String()
}
