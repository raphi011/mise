package ai_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// importEnv is a household with the services an import needs, fetching
// only from the test server's pages.
type importEnv struct {
	s     *ai.Service
	g     *groceries.Service
	r     *recipes.Service
	db    *store.DB
	sc    store.Scoped
	hh    storetest.Household
	items map[string]uuid.UUID
	pub   *userEvents
	// model is what the household's provider returns; nil fails.
	mu    sync.Mutex
	model ai.Model
}

// setModel sets what the provider returns; job workers read it.
func (e *importEnv) setModel(m ai.Model) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.model = m
}

// userEvents records events published to users.
type userEvents struct {
	mu     sync.Mutex
	events []live.Event
}

func (p *userEvents) PublishUser(user uuid.UUID, e live.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *userEvents) count(job uuid.UUID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.Kind == live.ImportChanged && e.JobID == job {
			n++
		}
	}
	return n
}

func newImportEnv(t *testing.T, db *store.DB, name string, f *ai.Fetcher) *importEnv {
	t.Helper()
	hub := live.NewHub()
	t.Cleanup(hub.Close)
	e := &importEnv{hh: storetest.NewHousehold(t, db, name), items: map[string]uuid.UUID{}, db: db, pub: &userEvents{}}
	e.sc = db.ScopeFor(e.hh.ID)
	e.g = groceries.New(hub, nil)
	e.r = recipes.New(hub, e.g, nil)
	e.s = ai.New(ai.Config{Box: newBox(t), Groceries: e.g, Recipes: e.r, Fetcher: f, DB: db, Publisher: e.pub,
		Provider: func(ai.Connection) (ai.Model, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.model == nil {
				return nil, errors.New("no model")
			}
			return e.model, nil
		}})
	return e
}

// item seeds catalogue items.
func (e *importEnv) item(t *testing.T, names ...string) {
	t.Helper()
	err := e.sc.Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		for _, n := range names {
			id, _, err := groceries.EnsureItem(ctx, q, e.sc, n, store.Now())
			if err != nil {
				return err
			}
			e.items[n] = id
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pages serves HTML bodies by path.
func pages(t *testing.T, bodies map[string]string) (base string, f *ai.Fetcher) {
	t.Helper()
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
	return srv.URL, fetcher(t, 0, srv)
}

const curryPage = `<!doctype html><html lang="de"><head>
<meta property="og:image" content="/curry.jpg">
<script type="application/ld+json">{"@type":"Recipe","name":"Kartoffelcurry",
 "recipeIngredient":["1,5 kg Kartoffeln","2 EL Öl","½ TL Salz"],
 "recipeInstructions":"Alles kochen."}</script></head><body></body></html>`

const blogPage = `<html><body><article><h1>Grandma's dal</h1><p>Take 250 g lentils.</p></article></body></html>`

func TestPageDraft(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		base, f := pages(t, map[string]string{"/dal": dalPage, "/curry": curryPage, "/blog": blogPage})
		e := newImportEnv(t, db, "home", f)
		other := newImportEnv(t, db, "other", f)
		e.item(t, "Red lentils", "Onion", "Sunflower oil")
		// The other household's exact "Oil" must not be seen.
		other.item(t, "Oil", "Garam masala")
		if _, err := e.r.CreateTag(ctx, e.sc, "Vegan"); err != nil {
			t.Fatal(err)
		}
		if _, err := other.r.CreateTag(ctx, other.sc, "Quick"); err != nil {
			t.Fatal(err)
		}

		d, page, err := e.s.PageDraft(ctx, e.sc, base+"/dal", language.German)
		if err != nil {
			t.Fatal(err)
		}
		if page.Recipe == nil || d.Name != "Red lentil dal & rice" || d.Servings != 4 || d.PrepMin != 10 || d.CookMin != 50 {
			t.Errorf("draft %+v", d)
		}
		if d.SourceURL != base+"/dal" || d.ImageURL != "https://cdn.food.example/dal.jpg" || len(d.Steps) != 3 {
			t.Errorf("source %q, image %q, %d steps", d.SourceURL, d.ImageURL, len(d.Steps))
		}
		want := []struct {
			item  string
			match ai.Match
		}{{"Red lentils", ai.MatchExact}, {"Onion", ai.MatchExact}, {"Sunflower oil", ai.MatchCheck}}
		if len(d.Ingredients) != len(want) {
			t.Fatalf("ingredients %+v", d.Ingredients)
		}
		for i, w := range want {
			ing := d.Ingredients[i]
			if ing.ItemName != w.item || ing.Match != w.match || ing.ItemID == nil || *ing.ItemID != e.items[w.item] {
				t.Errorf("ingredient %d: %+v, want %s %s", i, ing, w.item, w.match)
			}
		}
		// The page is English: "2 tbsp" parses although the importer reads German.
		if a := d.Ingredients[2].Amount; a.Quantity == nil || *a.Quantity != 2 || a.Unit != "tbsp" {
			t.Errorf("oil amount %+v", a)
		}
		if len(d.Tags) != 1 || d.Tags[0] != "Vegan" {
			t.Errorf("tags %q", d.Tags)
		}
		if len(d.NewItems()) != 0 {
			t.Errorf("new items %q", d.NewItems())
		}

		// German amounts on a lang="de" page; unknown items are new; og:image
		// fills in a missing photo.
		d, _, err = e.s.PageDraft(ctx, e.sc, base+"/curry", language.English)
		if err != nil {
			t.Fatal(err)
		}
		if d.ImageURL != base+"/curry.jpg" {
			t.Errorf("image %q", d.ImageURL)
		}
		amounts := []struct {
			qty  float64
			unit string
			name string
		}{{1.5, "kg", "Kartoffeln"}, {2, "tbsp", "Öl"}, {0.5, "tsp", "Salz"}}
		if len(d.Ingredients) != len(amounts) {
			t.Fatalf("ingredients %+v", d.Ingredients)
		}
		for i, w := range amounts {
			ing := d.Ingredients[i]
			if a := ing.Amount; a.Quantity == nil || *a.Quantity != w.qty || a.Unit != w.unit {
				t.Errorf("ingredient %d amount %+v, want %v %s", i, a, w.qty, w.unit)
			}
			if ing.Match != ai.MatchNew || ing.ItemID != nil || ing.ItemName != w.name {
				t.Errorf("ingredient %d: %+v", i, ing)
			}
		}
		if got := d.NewItems(); len(got) != 3 {
			t.Errorf("new items %q", got)
		}

		// No JSON-LD: ErrNoRecipe with the text for the model.
		_, page, err = e.s.PageDraft(ctx, e.sc, base+"/blog", language.English)
		if !errors.Is(err, ai.ErrNoRecipe) || !strings.Contains(page.Text, "Take 250 g lentils.") {
			t.Errorf("blog: %v, text %q", err, page.Text)
		}

		// Fetch errors pass through.
		if _, _, err := e.s.PageDraft(ctx, e.sc, base+"/missing", language.English); err == nil || errors.Is(err, ai.ErrNoRecipe) {
			t.Errorf("missing page: %v", err)
		}

		// The other household sees its own items and tags only.
		d, _, err = other.s.PageDraft(ctx, other.sc, base+"/dal", language.English)
		if err != nil {
			t.Fatal(err)
		}
		for _, ing := range d.Ingredients {
			if ing.ItemID != nil {
				for n, id := range e.items {
					if *ing.ItemID == id {
						t.Errorf("other household matched %s", n)
					}
				}
			}
		}
		if d.Ingredients[2].Match != ai.MatchExact || d.Ingredients[2].ItemName != "Oil" {
			t.Errorf("other household oil %+v", d.Ingredients[2])
		}
		if len(d.Tags) != 1 || d.Tags[0] != "Quick" {
			t.Errorf("other household tags %q", d.Tags)
		}
	})
}

// storetestEach runs fn on both engines with two households and a site
// serving /tofu, /print and /big (over the tool result cap). e has the
// items Firm tofu, Soy sauce and Onion and the tag Vegan; other has Garam
// masala and Oil.
func storetestEach(t *testing.T, fn func(t *testing.T, e, other *importEnv, base string)) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		base, f := pages(t, map[string]string{
			"/tofu":  `<html><body><p>Sticky tofu teaser. <a href="/print">Print</a></p></body></html>`,
			"/print": `<html><head><title>Print view</title></head><body><p>400 g firm tofu</p></body></html>`,
			"/big":   `<html><body><p>` + strings.Repeat("é", 40<<10) + `</p></body></html>`,
		})
		e := newImportEnv(t, db, "home", f)
		other := newImportEnv(t, db, "other", f)
		e.item(t, "Firm tofu", "Soy sauce", "Onion")
		other.item(t, "Garam masala", "Oil")
		if _, err := e.r.CreateTag(t.Context(), e.sc, "Vegan"); err != nil {
			t.Fatal(err)
		}
		fn(t, e, other, base)
	})
}

const notesPage = `<script type="application/ld+json">{"@type":"Recipe","name":"Fried rice",
 "recipeIngredient":["1 tbsp sunflower or vegetable oil plus a little extra for frying","2 tbsp tamari or soy sauce",
  "1 tsp sea salt to taste","3 spring onions, sliced","1 tbsp double or single cream"],
 "recipeInstructions":"Fry."}</script>`

func TestPageDraftNotesAndAlternatives(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		base, f := pages(t, map[string]string{"/rice": notesPage})
		e := newImportEnv(t, db, "home", f)
		e.item(t, "Vegetable oil", "Soy sauce", "Sea salt", "Spring onions")
		d, _, err := e.s.PageDraft(t.Context(), e.sc, base+"/rice", language.English)
		if err != nil {
			t.Fatal(err)
		}
		want := []struct {
			item  string
			match ai.Match
		}{
			{"Vegetable oil", ai.MatchCheck}, // the second alternative
			{"Soy sauce", ai.MatchCheck},
			{"Sea salt", ai.MatchExact}, // "to taste" is a note
			{"Spring onions", ai.MatchExact},
			{"Single cream", ai.MatchNew}, // "double" may be short for "double cream"
		}
		if len(d.Ingredients) != len(want) {
			t.Fatalf("ingredients %+v", d.Ingredients)
		}
		for i, w := range want {
			if ing := d.Ingredients[i]; ing.ItemName != w.item || ing.Match != w.match {
				t.Errorf("ingredient %d: %+v, want %s %s", i, ing, w.item, w.match)
			}
		}
	})
}
