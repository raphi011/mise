package discover_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/discover"
)

func site(t *testing.T, name string) discover.Site {
	t.Helper()
	for _, s := range discover.Sites() {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no site %s", name)
	return nil
}

func TestSitesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range discover.Sites() {
		if seen[s.Name()] {
			t.Errorf("%s listed twice", s.Name())
		}
		seen[s.Name()] = true
		if s.Language() == language.Und {
			t.Errorf("%s has no language", s.Name())
		}
	}
}

// TestCardSites reads each site's result page (saved from the site, cut
// down to three results) and checks the hits, the first in full.
func TestCardSites(t *testing.T) {
	for _, tc := range []struct {
		site, fixture, q string
		first            discover.Hit
		titles           []string
		more             bool
		// page2 is the address of the second result page.
		page2 string
	}{
		{
			site: "chefkoch.de", fixture: "chefkoch_results.html", q: "gulasch",
			first: discover.Hit{URL: "https://www.chefkoch.de/rezepte/431091134310048/Gulasch-nach-Oma-Magda.html", Title: "Gulasch nach Oma Magda",
				Photo:   "https://img.chefkoch-cdn.de/rezepte/431091134310048/bilder/1617138/crop-640x800/gulasch-nach-oma-magda.jpg",
				Snippet: "Zartes Rindergulasch ohne Anbraten"},
			titles: []string{"Gulasch nach Oma Magda", "Rindergulasch", "Brauhaus-Gulasch"},
			more:   true, page2: "https://www.chefkoch.de/rs/s1/gulasch/Rezepte.html",
		},
		{
			site: "gutekueche.de", fixture: "gutekueche_results.html", q: "gulasch",
			first: discover.Hit{URL: "https://www.gutekueche.de/omas-hirschgulasch-rezept-2008", Title: "Omas Hirschgulasch",
				Photo:   "https://cdn.gutekueche.de/media/recipe/103357/resp/omas-hirschgulasch___webp_780_520.webp",
				Snippet: "Omas Hirschgulasch ist einfach in der Zubereitung und schmeckt unglaublich gut. Dieses Rezept sollte man in der Wildsaison unbedingt ausprobieren."},
			titles: []string{"Omas Hirschgulasch", "Putengulasch", "Wildgulasch"},
			more:   true, page2: "https://www.gutekueche.de/suche?p=2&s=gulasch",
		},
		{
			site: "eatsmarter.de", fixture: "eatsmarter_results.html", q: "gulasch",
			first: discover.Hit{URL: "https://eatsmarter.de/rezepte/gulaschsuppe-nach-ungarischer-art", Title: "Gulaschsuppe nach ungarischer Art",
				Photo: "https://images.eatsmarter.de/sites/default/files/styles/300x225-webp/public/gulaschsuppe-nach-ungarischer-art-102723.jpg"},
			titles: []string{"Gulaschsuppe nach ungarischer Art", "Paprikagulasch mit Zwiebeln", "Schnelle Gulaschsuppe"},
			more:   true, page2: "https://eatsmarter.de/suche/rezepte?ft=gulasch&page=1",
		},
		{
			site: "bbcgoodfood.com", fixture: "bbcgoodfood_results.html", q: "goulash",
			first: discover.Hit{URL: "https://www.bbcgoodfood.com/recipes/beef-goulash", Title: "Beef goulash",
				Photo: "https://images.immediate.co.uk/production/volatile/sites/30/2020/08/beef-goulash-e7d0879.jpg?quality=90&resize=440%2C400&webp=true"},
			titles: []string{"Beef goulash", "Slow-cooker beef goulash", "One-pot goulash pasta"},
		},
		{
			site: "marmiton.org", fixture: "marmiton_results.html", q: "goulash",
			first: discover.Hit{URL: "https://www.marmiton.org/recettes/recette_goulash_13042.aspx", Title: "Goulash",
				Photo: "https://assets.afcdn.com/recipe/20180122/76981_w225h225c1cx4344cy2896cxt0cyt0cxb8688cyb5792.webp"},
			titles: []string{"Goulash", "Goulash de boeuf", "Goulash hongroise traditionnelle"},
			more:   true, page2: "https://www.marmiton.org/recettes/recherche.aspx?aqt=goulash&page=2",
		},
		{
			site: "750g.com", fixture: "750g_results.html", q: "goulash",
			first: discover.Hit{URL: "https://www.750g.com/goulash-a-la-hongroise-r23318.htm", Title: "Goulash à la hongroise traditionnel",
				Photo: "https://static.750g.com/images/210-120/bf4b1b64d3b1500bb929c538b06b00a5/file.jpg",
				Snippet: "coupez la viande en cubes, faites rissoler les oignons, lard, salez, saupoudrez de paprika et 25 cl d'eau, " +
					"mettre en cocotte et servez de pomme de terre et cr&egrav1"},
			titles: []string{"Goulash à la hongroise traditionnel", "Goulash du Chef Damien", "Soupe traditionnelle de Hongrie: la goulach"},
		},
		{
			site: "giallozafferano.it", fixture: "giallozafferano_results.html", q: "pollo al curry",
			first: discover.Hit{URL: "https://ricette.giallozafferano.it/Pollo-al-curry.html", Title: "Pollo al curry",
				Photo: "https://www.giallozafferano.it/images/242-24260/Pollo-al-curry_360x300.jpg"},
			titles: []string{"Pollo al curry", "Straccetti di pollo al curry", "Petto di pollo al curry"},
			more:   true, page2: "https://www.giallozafferano.it/ricerca-ricette/page2/pollo+al+curry/",
		},
	} {
		t.Run(tc.site, func(t *testing.T) {
			st := site(t, tc.site)
			u, kind := st.SearchURL(tc.q, 1)
			if kind != ai.KindPage {
				t.Errorf("kind %v", kind)
			}
			f := &fakeFetcher{pages: map[string]string{u: readFixture(t, tc.fixture)}}
			res := discover.New(f, []discover.Site{st}, nil).Search(t.Context(), tc.q, []string{tc.site}, 1)
			if res.Err != nil || len(res.Hits) == 0 {
				t.Fatalf("hits %v, err %v", res.Hits, res.Err)
			}
			tc.first.Site = tc.site
			if res.Hits[0] != tc.first {
				t.Errorf("first hit:\n got %+v\nwant %+v", res.Hits[0], tc.first)
			}
			var titles []string
			for _, h := range res.Hits {
				titles = append(titles, h.Title)
				if p, err := url.Parse(h.Photo); h.Photo != "" && (err != nil || !st.PhotoHost(p.Host)) {
					t.Errorf("%s: photo %s not on the photo host", h.Title, h.Photo)
				}
				if _, ok := st.RecipeURL(mustURL(t, h.URL)); !ok {
					t.Errorf("%s: %s is not a recipe page", h.Title, h.URL)
				}
			}
			if !slices.Equal(titles, tc.titles) {
				t.Errorf("titles %q, want %q", titles, tc.titles)
			}
			if res.More != tc.more {
				t.Errorf("more %v", res.More)
			}
			if tc.page2 != "" {
				if got, _ := st.SearchURL(tc.q, 2); got != tc.page2 {
					t.Errorf("page 2: %s, want %s", got, tc.page2)
				}
			}
		})
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestWordPress(t *testing.T) {
	st := site(t, "eat-this.org")
	u, kind := st.SearchURL("suppe", 2)
	if kind != ai.KindJSON || !strings.HasPrefix(u, "https://www.eat-this.org/wp-json/wp/v2/posts?") {
		t.Fatalf("search URL %s, kind %v", u, kind)
	}
	if q := mustURL(t, u).Query(); q.Get("search") != "suppe" || q.Get("page") != "2" || q.Get("per_page") != "20" || q.Get("_embed") != "wp:featuredmedia" {
		t.Errorf("query %v", q)
	}
	body := []byte(readFixture(t, "wordpress_results.json"))
	res, err := st.ParseResults(ai.Fetched{Body: body, Header: http.Header{"X-Wp-Totalpages": {"3"}}}, 2)
	if err != nil || len(res.Hits) != 2 || !res.More {
		t.Fatalf("hits %+v, more %v, err %v", res.Hits, res.More, err)
	}
	want := discover.Hit{URL: "https://www.eat-this.org/kichererbsen-minestrone-mit-orecchiette/", Title: "Kichererbsen-Minestrone mit Orecchiette",
		Photo: "https://www.eat-this.org/wp-content/uploads/2025/02/eat_this_kichererbsen-minestrone_mit_orecchiette_014-700x467.jpg"}
	got := res.Hits[0]
	if got.URL != want.URL || got.Title != want.Title || got.Photo != want.Photo {
		t.Errorf("first hit %+v", got)
	}
	// The excerpt loses its markup and entities and is cut at a word.
	if !strings.HasPrefix(got.Snippet, "Unsere Kichererbsen-Minestrone mit Orecchiette ist ein leckerer") || !strings.HasSuffix(got.Snippet, "…") ||
		strings.ContainsAny(got.Snippet, "<&") || len([]rune(got.Snippet)) > 201 {
		t.Errorf("snippet %q", got.Snippet)
	}
	// The last page has no more; without the header a full page has.
	if res, _ := st.ParseResults(ai.Fetched{Body: body, Header: http.Header{"X-Wp-Totalpages": {"3"}}}, 3); res.More {
		t.Error("more after the last page")
	}
	if res, _ := st.ParseResults(ai.Fetched{Body: body, Header: http.Header{}}, 1); res.More {
		t.Error("more after a short page")
	}
	if _, err := st.ParseResults(ai.Fetched{Body: []byte("<html>"), Header: http.Header{}}, 1); err == nil {
		t.Error("HTML read as results")
	}
	for raw, ok := range map[string]bool{
		"https://www.eat-this.org/vegane-bratwurst/":        true,
		"https://www.eat-this.org/2026/10/skillet-pierogi/": true,
		"https://www.eat-this.org/category/suppen/":         false,
		"https://www.eat-this.org/wp-content/uploads/x.jpg": false,
		"https://www.eat-this.org/vegane-bratwurst":         false,
		"https://evil.example/vegane-bratwurst/":            false,
		"https://www.eat-this.org/":                         false,
	} {
		if _, got := st.RecipeURL(mustURL(t, raw)); got != ok {
			t.Errorf("%s: recipe %v", raw, got)
		}
	}
	if !st.PhotoHost("www.eat-this.org") || st.PhotoHost("eat-this.org") {
		t.Error("photo host")
	}
}

func TestCardSiteRecipeURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.chefkoch.de/rezepte/2275401363075693/Rindergulasch.html?portionen=4": "https://www.chefkoch.de/rezepte/2275401363075693/Rindergulasch.html",
		"https://www.chefkoch.de/rs/s0/gulasch/Rezepte.html":                              "",
		"http://ricette.giallozafferano.it/Pollo-al-curry.html":                           "https://ricette.giallozafferano.it/Pollo-al-curry.html",
		"https://www.giallozafferano.it/ricette-cat/Primi/":                               "",
		"https://www.bbcgoodfood.com/recipes/collection/goulash-recipes":                  "",
		"https://eatsmarter.de/rezepte/zutaten/apfel-rezepte":                             "",
	} {
		u := mustURL(t, raw)
		var got string
		for _, s := range discover.Sites() {
			if r, ok := s.RecipeURL(u); ok {
				got = r
			}
		}
		if got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
