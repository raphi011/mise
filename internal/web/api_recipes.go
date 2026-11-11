package web

import (
	"bytes"
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
}

func cardJSON(c recipes.Card) recipeCardJSON {
	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	return recipeCardJSON{
		ID: c.ID.String(), Name: c.Name, TotalMin: c.TotalMin, Servings: c.Servings, Tags: tags, Favourite: c.Favourite,
		CreatedBy: c.CreatedBy.Name, TimesCooked: c.TimesCooked, LastCookedAt: c.LastCookedAt,
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
		Servings: r.Servings, SourceURL: r.SourceURL, Notes: r.Notes, Favourite: r.Favourite,
		CreatedBy: r.CreatedBy.Name, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Tags: []string{}, Ingredients: []ingredientJSON{}, Steps: []stepJSON{}, Links: []linkJSON{},
	}
	for _, t := range r.Tags {
		v.Tags = append(v.Tags, t.Name)
	}
	for _, i := range r.Ingredients {
		v.Ingredients = append(v.Ingredients, ingredientJSON{ID: i.ID.String(), Name: i.ItemName, Amount: amountText(i.Amount, lang), Optional: i.Optional})
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
// ingredients, steps and links not listed are removed.
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
		SourceURL: in.SourceURL, Notes: in.Notes, Tags: in.Tags, Links: in.Links,
	}
	for _, i := range in.Ingredients {
		var row recipes.IngredientInput
		if i.line != "" {
			row = recipes.ParseIngredient(i.line, lang)
		} else {
			row = recipes.IngredientInput{Name: i.Name, Amount: groceries.ParseAmount(i.Amount, lang)}
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

// apiListRecipes lists recipes: ?q= searches names and ingredients, else
// the library in ?sort= order (cooked, added, most, name, quickest), at
// most ?limit= (default 50, max 200).
func (s *Server) apiListRecipes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	viewer := identity(r).UserID
	limit := intParam(r, "limit", 50, 200)
	if limit == 0 {
		limit = 50
	}
	out := struct {
		Recipes []recipeCardJSON `json:"recipes"`
		// Total is how many recipes match, whatever the limit.
		Total int `json:"total"`
	}{Recipes: []recipeCardJSON{}}
	if q := r.URL.Query().Get("q"); q != "" {
		res, err := s.recipes.Search(ctx, sc, viewer, q)
		if err != nil {
			s.apiErr(w, r, err)
			return
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
		writeJSON(w, http.StatusOK, out)
		return
	}
	f := recipes.Filter{Sort: recipes.Sort(r.URL.Query().Get("sort"))}
	cards, total, err := s.recipes.LibraryPage(ctx, sc, viewer, f, limit)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	out.Total = total
	for _, c := range cards {
		out.Recipes = append(out.Recipes, cardJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiGetRecipe(w http.ResponseWriter, r *http.Request) {
	s.writeRecipe(w, r, http.StatusOK, "")
}

// writeRecipe answers with the recipe named by id, or by the path when id
// is empty.
func (s *Server) writeRecipe(w http.ResponseWriter, r *http.Request, status int, id string) {
	ctx := r.Context()
	if id == "" {
		id = r.PathValue("id")
	}
	recipeID, err := uuid.Parse(id)
	if err != nil {
		apiNotFound(w, r)
		return
	}
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	rec, err := s.recipes.Get(ctx, sc, recipeID, identity(r).UserID)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	writeJSON(w, status, recipeToJSON(rec, i18n.Locale(ctx)))
}

func (s *Server) apiCreateRecipe(w http.ResponseWriter, r *http.Request) {
	var in recipeIn
	if !decodeJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	id, err := s.recipes.Create(ctx, sc, identity(r).UserID, in.input(i18n.Locale(ctx)))
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/recipes/"+id.String())
	s.writeRecipe(w, r, http.StatusCreated, id.String())
}

func (s *Server) apiUpdateRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	var in recipeIn
	if !decodeJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	sc, err := s.db.Scope(ctx)
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if err := s.recipes.Update(ctx, sc, recipeID, identity(r).UserID, in.input(i18n.Locale(ctx))); err != nil {
		s.apiErr(w, r, err)
		return
	}
	s.writeRecipe(w, r, http.StatusOK, "")
}

func (s *Server) apiDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	recipeID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apiNotFound(w, r)
		return
	}
	sc, err := s.db.Scope(r.Context())
	if err != nil {
		s.apiErr(w, r, err)
		return
	}
	if err := s.recipes.Delete(r.Context(), sc, recipeID); err != nil {
		s.apiErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
