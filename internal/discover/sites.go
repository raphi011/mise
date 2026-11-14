package discover

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html/atom"
	"golang.org/x/text/language"
)

// Sites are the recipe sites mise can search, grouped by language. Their
// order is the order hits are interleaved in.
func Sites() []Site {
	return []Site{
		Chefkoch(),
		Ichkoche(),
		cardSite{
			name: "gutekueche.de", lang: language.German,
			search: func(q string, page int) string {
				return withPage("https://www.gutekueche.de/suche", url.Values{"s": {q}}, "p", page, 1)
			},
			recipeHost: "www.gutekueche.de", recipePath: regexp.MustCompile(`^/[a-z0-9-]+-rezept-\d+$`),
			photoHosts: []string{"cdn.gutekueche.de"},
			snippet:    byTag(atom.P),
		},
		cardSite{
			name: "eatsmarter.de", lang: language.German,
			search: func(q string, page int) string {
				return withPage("https://eatsmarter.de/suche/rezepte", url.Values{"ft": {q}}, "page", page, 0)
			},
			recipeHost: "eatsmarter.de", recipePath: regexp.MustCompile(`^/rezepte/[a-z0-9-]+$`),
			photoHosts: []string{"images.eatsmarter.de"},
			itemList:   true,
			next:       queryPage("/suche/rezepte", "page", 0),
		},
		wordPress{name: "emmikochteinfach.de", lang: language.German, base: &url.URL{Scheme: "https", Host: "emmikochteinfach.de"}},
		wordPress{name: "eat-this.org", lang: language.German, base: &url.URL{Scheme: "https", Host: "www.eat-this.org"}},

		cardSite{
			name: "bbcgoodfood.com", lang: language.English,
			// Only the first result page is rendered on the server.
			search: func(q string, _ int) string {
				return "https://www.bbcgoodfood.com/search?" + url.Values{"q": {q}}.Encode()
			},
			recipeHost: "www.bbcgoodfood.com", recipePath: regexp.MustCompile(`^/recipes/[a-z0-9-]+$`),
			photoHosts: []string{"images.immediate.co.uk"},
			// Result cards ask for a 93 px thumbnail.
			photoSize: func(u *url.URL) {
				q := u.Query()
				if q.Has("resize") {
					q.Set("resize", "440,400")
					u.RawQuery = q.Encode()
				}
			},
		},
		wordPress{name: "recipetineats.com", lang: language.English, base: &url.URL{Scheme: "https", Host: "www.recipetineats.com"}},
		wordPress{name: "smittenkitchen.com", lang: language.English, base: &url.URL{Scheme: "https", Host: "smittenkitchen.com"}},
		wordPress{name: "minimalistbaker.com", lang: language.English, base: &url.URL{Scheme: "https", Host: "minimalistbaker.com"}},

		cardSite{
			name: "marmiton.org", lang: language.French,
			search: func(q string, page int) string {
				return withPage("https://www.marmiton.org/recettes/recherche.aspx", url.Values{"aqt": {q}}, "page", page, 1)
			},
			recipeHost: "www.marmiton.org", recipePath: regexp.MustCompile(`^/recettes/recette_[a-z0-9-]+_\d+\.aspx$`),
			photoHosts: []string{"assets.afcdn.com"},
			itemList:   true,
			next:       queryPage("/recettes/recherche.aspx", "page", 1),
		},
		cardSite{
			name: "750g.com", lang: language.French,
			// Further result pages are loaded by script, with no link to them.
			search: func(q string, _ int) string {
				return "https://www.750g.com/recherche/?" + url.Values{"q": {q}}.Encode()
			},
			recipeHost: "www.750g.com", recipePath: regexp.MustCompile(`^/[a-z0-9-]+-r\d+\.htm$`),
			photoHosts: []string{"static.750g.com"},
			snippet:    byClass("news-card-text"),
		},

		cardSite{
			name: "giallozafferano.it", lang: language.Italian,
			search: func(q string, page int) string {
				p := "https://www.giallozafferano.it/ricerca-ricette/"
				if page > 1 {
					p += "page" + strconv.Itoa(page) + "/"
				}
				return p + url.QueryEscape(q) + "/"
			},
			recipeHost: "ricette.giallozafferano.it", recipePath: regexp.MustCompile(`^/[A-Za-z0-9-]+\.html$`),
			photoHosts: []string{"www.giallozafferano.it"},
			itemList:   true,
			next: func(u *url.URL, page int) bool {
				return strings.HasPrefix(u.Path, "/ricerca-ricette/page"+strconv.Itoa(page+1)+"/")
			},
		},
	}
}

// Chefkoch is chefkoch.de. Its result pages count from s0 in the path
// (/rs/s0/<query>/Rezepte.html) and list their results in an ItemList.
func Chefkoch() Site {
	return cardSite{
		name: "chefkoch.de", lang: language.German,
		search: func(q string, page int) string {
			return "https://www.chefkoch.de/rs/s" + strconv.Itoa(page-1) + "/" + url.QueryEscape(q) + "/Rezepte.html"
		},
		recipeHost: "www.chefkoch.de", recipePath: regexp.MustCompile(`^/rezepte/\d+/[^/]+\.html$`),
		photoHosts: []string{"img.chefkoch-cdn.de"},
		itemList:   true,
		next: func(u *url.URL, page int) bool {
			return strings.HasPrefix(u.Path, "/rs/s"+strconv.Itoa(page)+"/")
		},
	}
}

// withPage is base?v with param set for pages after the first, numbered
// from first.
func withPage(base string, v url.Values, param string, page, first int) string {
	if page > 1 {
		v.Set(param, strconv.Itoa(page-1+first))
	}
	return base + "?" + v.Encode()
}

// queryPage accepts links to path whose param names the page after page,
// pages numbered from first.
func queryPage(path, param string, first int) func(*url.URL, int) bool {
	return func(u *url.URL, page int) bool {
		return u.Path == path && u.Query().Get(param) == strconv.Itoa(page+first)
	}
}
