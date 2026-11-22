package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/recipes"
)

// amountText is an amount as one string: "2 tbsp", "½ bunch", "a pinch".
func amountText(a groceries.Amount, lang language.Tag) string {
	if a.Quantity != nil {
		return groceries.FormatAmount(*a.Quantity, a.Unit, lang)
	}
	return a.Text
}

type recipeCardJSON struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	TotalMin     *int       `json:"total_min"`
	Servings     int        `json:"servings"`
	Tags         []string   `json:"tags"`
	Favourite    bool       `json:"favourite"`
	CreatedBy    string     `json:"created_by"`
	TimesCooked  int        `json:"times_cooked"`
	LastCookedAt *time.Time `json:"last_cooked_at"`
	// PhotoURL is the API path of the photo, or null.
	PhotoURL *string `json:"photo_url"`
}

func cardJSON(c recipes.Card) recipeCardJSON {
	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	return recipeCardJSON{
		ID: c.ID.String(), Name: c.Name, TotalMin: c.TotalMin, Servings: c.Servings, Tags: tags, Favourite: c.Favourite,
		CreatedBy: c.CreatedBy.Name, TimesCooked: c.TimesCooked, LastCookedAt: c.LastCookedAt, PhotoURL: photoURL(c.ID, c.Photo),
	}
}

type ingredientJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Amount   string `json:"amount"`
	Optional bool   `json:"optional"`
}

type stepJSON struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	TimerSeconds *int   `json:"timer_seconds"`
}

type recipeJSON struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	PrepMin     *int             `json:"prep_min"`
	CookMin     *int             `json:"cook_min"`
	Servings    int              `json:"servings"`
	SourceURL   string           `json:"source_url"`
	Notes       string           `json:"notes"`
	PhotoURL    *string          `json:"photo_url"`
	Tags        []string         `json:"tags"`
	Ingredients []ingredientJSON `json:"ingredients"`
	Steps       []stepJSON       `json:"steps"`
	// Links are recipes that go well with this one.
	Links     []linkJSON `json:"links"`
	Favourite bool       `json:"favourite"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type linkJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func recipeToJSON(r recipes.Recipe, lang language.Tag) recipeJSON {
	v := recipeJSON{
		ID: r.ID.String(), Name: r.Name, Description: r.Description, PrepMin: r.PrepMin, CookMin: r.CookMin,
		Servings: r.Servings, SourceURL: r.SourceURL, Notes: r.Notes, PhotoURL: photoURL(r.ID, r.Photo), Favourite: r.Favourite,
		CreatedBy: r.CreatedBy.Name, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Tags: []string{}, Ingredients: []ingredientJSON{}, Steps: []stepJSON{}, Links: []linkJSON{},
	}
	for _, t := range r.Tags {
		v.Tags = append(v.Tags, t.Name)
	}
	for _, i := range r.Ingredients {
		v.Ingredients = append(v.Ingredients, ingredientJSON{ID: i.ID.String(), Name: i.ItemName, Amount: recipes.AmountField(i.Amount, i.Note, lang), Optional: i.Optional})
	}
	for _, s := range r.Steps {
		v.Steps = append(v.Steps, stepJSON{ID: s.ID.String(), Text: s.Text, TimerSeconds: s.TimerSeconds})
	}
	for _, l := range r.Links {
		v.Links = append(v.Links, linkJSON{ID: l.ID.String(), Name: l.Name})
	}
	return v
}

// ingredientIn is an ingredient of a recipe being written: either a line
// like "2 tbsp soy sauce" or an object {id, name, amount, optional}. With
// an id the row keeps that ingredient of the recipe.
type ingredientIn struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Amount   string    `json:"amount"`
	Optional bool      `json:"optional"`
	line     string
}

func (i *ingredientIn) UnmarshalJSON(b []byte) error {
	if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &i.line)
	}
	type plain ingredientIn
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode((*plain)(i))
}

// stepIn is a step: a text, or an object {text, timer_seconds}.
type stepIn struct {
	Text         string `json:"text"`
	TimerSeconds *int   `json:"timer_seconds"`
}

func (s *stepIn) UnmarshalJSON(b []byte) error {
	if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &s.Text)
	}
	type plain stepIn
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode((*plain)(s))
}

// recipeIn is a recipe being written. PUT replaces everything: tags,
// ingredients, steps and links not listed are removed. The photo has its
// own endpoints and stays.
type recipeIn struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PrepMin     *int           `json:"prep_min"`
	CookMin     *int           `json:"cook_min"`
	Servings    int            `json:"servings"`
	SourceURL   string         `json:"source_url"`
	Notes       string         `json:"notes"`
	Tags        []string       `json:"tags"`
	Ingredients []ingredientIn `json:"ingredients"`
	Steps       []stepIn       `json:"steps"`
	Links       []uuid.UUID    `json:"links"`
}

func (in recipeIn) input(lang language.Tag) recipes.Input {
	out := recipes.Input{
		Name: in.Name, Description: in.Description, PrepMin: in.PrepMin, CookMin: in.CookMin, Servings: in.Servings,
		SourceURL: in.SourceURL, Notes: in.Notes, Tags: in.Tags, Links: in.Links, KeepPhoto: true,
	}
	for _, i := range in.Ingredients {
		var row recipes.IngredientInput
		if i.line != "" {
			row = recipes.ParseIngredient(i.line, lang)
		} else {
			amount, note := groceries.ParseIngredientAmount(i.Amount, lang)
			row = recipes.IngredientInput{Name: i.Name, Amount: amount, Note: note}
		}
		row.ID, row.Optional = i.ID, i.Optional
		out.Ingredients = append(out.Ingredients, row)
	}
	for _, s := range in.Steps {
		out.Steps = append(out.Steps, recipes.StepInput{Text: s.Text, TimerSeconds: s.TimerSeconds})
	}
	return out
}

// intParam reads a non-negative integer query parameter, or def.
func intParam(r *http.Request, name string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < 0 {
		return def
	}
	return min(n, max)
}

// recipeListJSON is a page of recipe cards.
type recipeListJSON struct {
	Recipes []recipeCardJSON `json:"recipes"`
	// Total is how many recipes match, whatever the limit.
	Total int `json:"total"`
}

// doListRecipes lists recipes: q searches names and ingredients, else the
// library in sort order (cooked, added, most, name, quickest), at most
// limit (0 means 50).
func (s *Server) doListRecipes(ctx context.Context, q, sort string, limit int) (recipeListJSON, error) {
	out := recipeListJSON{Recipes: []recipeCardJSON{}}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return out, err
	}
	viewer := ctxIdentity(ctx).UserID
	if limit <= 0 {
		limit = 50
	}
	if q != "" {
		res, err := s.recipes.Search(ctx, sc, viewer, q)
		if err != nil {
			return out, err
		}
		seen := map[uuid.UUID]bool{}
		var cards []recipes.Card
		for _, m := range res.Names {
			seen[m.ID] = true
			cards = append(cards, m.Card)
		}
		for _, m := range res.Ingredients {
			if !seen[m.ID] {
				seen[m.ID] = true
				cards = append(cards, m.Card)
			}
		}
		out.Total = len(cards)
		for _, c := range cards[:min(limit, len(cards))] {
			out.Recipes = append(out.Recipes, cardJSON(c))
		}
		return out, nil
	}
	cards, total, err := s.recipes.LibraryPage(ctx, sc, viewer, recipes.Filter{Sort: recipes.Sort(sort)}, limit)
	if err != nil {
		return out, err
	}
	out.Total = total
	for _, c := range cards {
		out.Recipes = append(out.Recipes, cardJSON(c))
	}
	return out, nil
}

func (s *Server) doGetRecipe(ctx context.Context, recipeID uuid.UUID) (recipeJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return recipeJSON{}, err
	}
	rec, err := s.recipes.Get(ctx, sc, recipeID, ctxIdentity(ctx).UserID)
	if err != nil {
		return recipeJSON{}, err
	}
	return recipeToJSON(rec, i18n.Locale(ctx)), nil
}

func (s *Server) doCreateRecipe(ctx context.Context, in recipeIn) (recipeJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return recipeJSON{}, err
	}
	id, err := s.recipes.Create(ctx, sc, ctxIdentity(ctx).UserID, in.input(i18n.Locale(ctx)))
	if err != nil {
		return recipeJSON{}, err
	}
	return s.doGetRecipe(ctx, id)
}

func (s *Server) doUpdateRecipe(ctx context.Context, recipeID uuid.UUID, in recipeIn) (recipeJSON, error) {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return recipeJSON{}, err
	}
	if err := s.recipes.Update(ctx, sc, recipeID, ctxIdentity(ctx).UserID, in.input(i18n.Locale(ctx))); err != nil {
		return recipeJSON{}, err
	}
	return s.doGetRecipe(ctx, recipeID)
}

func (s *Server) doDeleteRecipe(ctx context.Context, recipeID uuid.UUID) error {
	sc, err := s.db.Scope(ctx)
	if err != nil {
		return err
	}
	return s.recipes.Delete(ctx, sc, recipeID)
}

// apiListRecipes lists recipes: ?q= searches names and ingredients, else
// the library in ?sort= order, at most ?limit= (default 50, max 200).
func (s *Server) apiListRecipes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.doListRecipes(r.Context(), q.Get("q"), q.Get("sort"), intParam(r, "limit", 50, 200))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiGetRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	out, err := s.doGetRecipe(r.Context(), recipeID)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiCreateRecipe(w http.ResponseWriter, r *http.Request) {
	var in recipeIn
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := s.doCreateRecipe(r.Context(), in)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/recipes/"+out.ID)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) apiUpdateRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	var in recipeIn
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := s.doUpdateRecipe(r.Context(), recipeID, in)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, err := parseID(r.PathValue("id"))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if err := s.doDeleteRecipe(r.Context(), recipeID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
