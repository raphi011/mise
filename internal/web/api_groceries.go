package web

import (
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

func (s *Server) apiLists(w http.ResponseWriter, r *http.Request) {
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	lists, err := s.groceries.Lists(r.Context(), sc)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	out := struct {
		Lists []listJSON `json:"lists"`
	}{Lists: []listJSON{}}
	for _, l := range lists {
		out.Lists = append(out.Lists, listJSON{ID: l.ID.String(), Name: l.Name, Open: l.Open})
	}
	writeJSON(w, http.StatusOK, out)
}

// apiList answers with a list's entries: those still to get in aisle
// order, then the cart.
func (s *Server) apiList(w http.ResponseWriter, r *http.Request) {
	listID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	view, err := s.groceries.ListView(ctx, sc, listID)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	lang := i18n.Locale(ctx)
	out := struct {
		ID      string      `json:"id"`
		Name    string      `json:"name"`
		Entries []entryJSON `json:"entries"`
	}{ID: view.List.ID.String(), Name: view.List.Name, Entries: []entryJSON{}}
	for _, g := range view.Groups {
		for _, e := range g.Entries {
			out.Entries = append(out.Entries, entryToJSON(e, lang))
		}
	}
	for _, e := range view.Cart {
		out.Entries = append(out.Entries, entryToJSON(e, lang))
	}
	writeJSON(w, http.StatusOK, out)
}

// apiAddEntry puts an item on a list: {"text": "2 tbsp soy sauce"} is read
// like the add field; {"name": "soy sauce", "amount": "2 tbsp"} is explicit.
// An item already on the list gets the amount added when units match.
func (s *Server) apiAddEntry(w http.ResponseWriter, r *http.Request) {
	listID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	var in struct {
		Text   string `json:"text"`
		Name   string `json:"name"`
		Amount string `json:"amount"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	lang := i18n.Locale(ctx)
	if (in.Text == "") == (in.Name == "") {
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "Send either text or name.", map[string]string{"name": "Send either text or name."})
		return
	}
	add := groceries.AddInput{ListID: listID, AddedBy: identity(r).UserID, Lang: lang}
	if in.Text != "" {
		add.Amount, add.Name = groceries.ParseEntry(in.Text, lang)
	} else {
		add.Name, add.Amount = in.Name, groceries.ParseAmount(in.Amount, lang)
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	added, err := s.groceries.Add(ctx, sc, add)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entryToJSON(added.Entry, lang))
}

func (s *Server) apiEntryAction(w http.ResponseWriter, r *http.Request, do func(*groceries.Service, store.Scoped, uuid.UUID) (groceries.Entry, error)) {
	entryID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	e, err := do(s.groceries, sc, entryID)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entryToJSON(e, i18n.Locale(r.Context())))
}

func (s *Server) apiCheckEntry(w http.ResponseWriter, r *http.Request) {
	s.apiEntryAction(w, r, func(g *groceries.Service, sc store.Scoped, id uuid.UUID) (groceries.Entry, error) {
		return g.Check(r.Context(), sc, id)
	})
}

func (s *Server) apiUncheckEntry(w http.ResponseWriter, r *http.Request) {
	s.apiEntryAction(w, r, func(g *groceries.Service, sc store.Scoped, id uuid.UUID) (groceries.Entry, error) {
		return g.Uncheck(r.Context(), sc, id)
	})
}

func (s *Server) apiRemoveEntry(w http.ResponseWriter, r *http.Request) {
	entryID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if _, err := s.groceries.Remove(r.Context(), sc, entryID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
