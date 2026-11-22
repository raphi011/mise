package web

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
)

type listJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Open is how many entries are still to get.
	Open int `json:"open"`
}

type entryJSON struct {
	ID       string `json:"id"`
	ListID   string `json:"list_id"`
	Name     string `json:"name"`
	Amount   string `json:"amount"`
	Category string `json:"category"`
	// Checked entries are in the cart.
	Checked   bool       `json:"checked"`
	CheckedAt *time.Time `json:"checked_at"`
	AddedBy   string     `json:"added_by"`
	Recipe    string     `json:"recipe"`
}

func entryToJSON(e groceries.Entry, lang language.Tag) entryJSON {
	v := entryJSON{
		ID: e.ID.String(), ListID: e.ListID.String(), Name: e.ItemName, Amount: amountText(e.Amount, lang),
		Checked: e.Checked(), CheckedAt: e.CheckedAt, AddedBy: e.AddedByName, Recipe: e.RecipeName,
	}
	if e.Category != nil {
		v.Category = e.Category.Name
	}
	return v
}

type listsJSON struct {
	Lists []listJSON `json:"lists"`
}

type listViewJSON struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Entries []entryJSON `json:"entries"`
}

// addEntryIn puts an item on a list: Text is read like the add field
// ("2 tbsp soy sauce"); Name and Amount are explicit.
type addEntryIn struct {
	Text   string `json:"text"`
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

func (s *Server) doLists(ctx context.Context) (listsJSON, error) {
	out := listsJSON{Lists: []listJSON{}}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return out, err
	}
	lists, err := s.groceries.Lists(ctx, sc)
	if err != nil {
		return out, err
	}
	for _, l := range lists {
		out.Lists = append(out.Lists, listJSON{ID: l.ID.String(), Name: l.Name, Open: l.Open})
	}
	return out, nil
}

// doList returns a list's entries: those still to get in aisle order, then
// the cart.
func (s *Server) doList(ctx context.Context, listID uuid.UUID) (listViewJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return listViewJSON{}, err
	}
	view, err := s.groceries.ListView(ctx, sc, listID)
	if err != nil {
		return listViewJSON{}, err
	}
	lang := i18n.Locale(ctx)
	out := listViewJSON{ID: view.List.ID.String(), Name: view.List.Name, Entries: []entryJSON{}}
	for _, g := range view.Groups {
		for _, e := range g.Entries {
			out.Entries = append(out.Entries, entryToJSON(e, lang))
		}
	}
	for _, e := range view.Cart {
		out.Entries = append(out.Entries, entryToJSON(e, lang))
	}
	return out, nil
}

// doAddEntry puts an item on a list. An item already on the list gets the
// amount added when units match.
func (s *Server) doAddEntry(ctx context.Context, listID uuid.UUID, in addEntryIn) (entryJSON, error) {
	lang := i18n.Locale(ctx)
	if (in.Text == "") == (in.Name == "") {
		return entryJSON{}, &apiProblem{status: http.StatusUnprocessableEntity, code: "invalid", message: "Send either text or name.",
			fields: map[string]string{"name": "Send either text or name."}}
	}
	add := groceries.AddInput{ListID: listID, AddedBy: ctxIdentity(ctx).UserID, Lang: lang}
	if in.Text != "" {
		add.Amount, add.Name = groceries.ParseEntry(in.Text, lang)
	} else {
		add.Name, add.Amount = in.Name, groceries.ParseAmount(in.Amount, lang)
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return entryJSON{}, err
	}
	added, err := s.groceries.Add(ctx, sc, add)
	if err != nil {
		return entryJSON{}, err
	}
	return entryToJSON(added.Entry, lang), nil
}

// entryAction is a change to one entry.
type entryAction func(*groceries.Service, context.Context, store.Scoped, uuid.UUID) (groceries.Entry, error)

var (
	checkEntry   entryAction = (*groceries.Service).Check
	uncheckEntry entryAction = (*groceries.Service).Uncheck
)

func (s *Server) doEntryAction(ctx context.Context, entryID uuid.UUID, do entryAction) (entryJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return entryJSON{}, err
	}
	e, err := do(s.groceries, ctx, sc, entryID)
	if err != nil {
		return entryJSON{}, err
	}
	return entryToJSON(e, i18n.Locale(ctx)), nil
}

func (s *Server) doRemoveEntry(ctx context.Context, entryID uuid.UUID) error {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return err
	}
	_, err = s.groceries.Remove(ctx, sc, entryID)
	return err
}

func (s *Server) apiLists(w http.ResponseWriter, r *http.Request) {
	out, err := s.doLists(r.Context())
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request) {
	listID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	out, err := s.doList(r.Context(), listID)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiAddEntry(w http.ResponseWriter, r *http.Request) {
	listID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	var in addEntryIn
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := s.doAddEntry(r.Context(), listID, in)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) apiEntryAction(w http.ResponseWriter, r *http.Request, do entryAction) {
	entryID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	out, err := s.doEntryAction(r.Context(), entryID, do)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiCheckEntry(w http.ResponseWriter, r *http.Request) {
	s.apiEntryAction(w, r, checkEntry)
}

func (s *Server) apiUncheckEntry(w http.ResponseWriter, r *http.Request) {
	s.apiEntryAction(w, r, uncheckEntry)
}

func (s *Server) apiRemoveEntry(w http.ResponseWriter, r *http.Request) {
	entryID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if err := s.doRemoveEntry(r.Context(), entryID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
