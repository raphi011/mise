package web

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/discover"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
	"github.com/raphi011/mise/internal/ui"
)

var discoverJobRe = regexp.MustCompile(`href="(/imports/[0-9a-f-]{36})"`)

// recipePage is a recipe page with microdata, as both test sites serve
// them.
func recipePage(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<html lang="en"><body><main itemscope itemtype="https://schema.org/Recipe">
<h1 itemprop="name">%s</h1><meta itemprop="recipeYield" content="2"><meta itemprop="totalTime" content="PT25M">
<img itemprop="image" src="/photo.png" alt="">
<meta itemprop="recipeIngredient" content="400 g firm tofu"><meta itemprop="recipeIngredient" content="1 tbsp oil">
<ol><li itemprop="recipeInstructions" itemscope itemtype="https://schema.org/HowToStep"><span itemprop="text">Fry the tofu.</span></li></ol>
</main></body></html>`, name)
}

// searchSite stands in for ichkoche.at: a result page, recipe pages with
// microdata and a photo, all on one loopback host. searches counts the
// result pages served.
func searchSite(t *testing.T, searches *atomic.Int32) *httptest.Server {
	t.Helper()
	png := pngBytes(t)
	var site *httptest.Server
	site = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/suche/rezepte":
			searches.Add(1)
			switch r.URL.Query().Get("q") {
			case "boom":
				http.Error(w, "down", http.StatusInternalServerError)
				return
			case "tofu":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = fmt.Fprintf(w, `<html><body>
<article class="box"><a href="/crispy-tofu-rezept-1"><img data-src="%[1]s/photo.png"></a>
 <div class="content"><h3><a href="/crispy-tofu-rezept-1">Crispy tofu</a></h3></div>
 <div class="recipe_info"><div class="rating"><p class="stars stars_5"></p><span>(12 Bewertungen)</span></div>
 <p class="spoons spoons_1"><span>Eier-Koch</span></p></div></article>
<article class="box"><a href="/tofu-scramble-rezept-2"></a><div class="content"><h3>Tofu scramble</h3></div></article>
</body></html>`, site.URL)
			default:
				_, _ = w.Write([]byte(`<html><body><h1>Keine Ergebnisse</h1></body></html>`))
			}
		case "/crispy-tofu-rezept-1":
			recipePage(w, "Crispy tofu")
		case "/tofu-scramble-rezept-2":
			recipePage(w, "Tofu scramble")
		case "/plain-rezept-3":
			_, _ = w.Write([]byte(`<html><body><p>Just a story about tofu.</p></body></html>`))
		case "/photo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site
}

// listSite is a second, made-up recipe site: its result page lists
// <a class="hit"> links, with <a rel="next"> when there is a next page.
type listSite struct {
	name string
	base *url.URL
}

func (s listSite) Name() string { return s.name }

func (s listSite) SearchURL(q string, page int) string {
	return s.base.JoinPath("search").String() + "?" + url.Values{"q": {q}, "p": {strconv.Itoa(page)}}.Encode()
}

func (s listSite) ParseResults(doc *xhtml.Node, base *url.URL) discover.ResultPage {
	var res discover.ResultPage
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "a" {
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[a.Key] = a.Val
			}
			if attrs["rel"] == "next" {
				res.More = true
			}
			if u, err := base.Parse(attrs["href"]); err == nil && attrs["class"] == "hit" && n.FirstChild != nil {
				if link, ok := s.RecipeURL(u); ok {
					res.Hits = append(res.Hits, discover.Hit{URL: link, Title: n.FirstChild.Data})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return res
}

func (s listSite) RecipeURL(u *url.URL) (string, bool) {
	if u.Host != s.base.Host || !strings.HasPrefix(u.Path, "/r/") {
		return "", false
	}
	return s.base.JoinPath(u.Path).String(), true
}

func (s listSite) PhotoHost(string) bool { return false }

// otherSite serves listSite's pages: two pages for "tofu", one for "boom".
func otherSite(t *testing.T, searches *atomic.Int32) *httptest.Server {
	t.Helper()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			searches.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			switch q, p := r.URL.Query().Get("q"), r.URL.Query().Get("p"); {
			case q == "tofu" && p == "1":
				_, _ = w.Write([]byte(`<a class="hit" href="/r/tofu-curry">Tofu curry</a><a rel="next" href="?p=2">next</a>`))
			case q == "tofu" && p == "2":
				_, _ = w.Write([]byte(`<a class="hit" href="/r/tofu-stew">Tofu stew</a>`))
			case q == "boom":
				_, _ = w.Write([]byte(`<a class="hit" href="/r/boom-bowl">Boom bowl</a>`))
			}
		case strings.HasPrefix(r.URL.Path, "/r/"):
			recipePage(w, "Tofu curry")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site
}

// sites rebuilds s's recipe site search with ichkoche.at and a second
// site on loopback servers.
type sites struct {
	ich, other        *httptest.Server
	ichHits, otherHit atomic.Int32
}

func withSites(t *testing.T, s *Server) *sites {
	t.Helper()
	ss := &sites{}
	ss.ich, ss.other = searchSite(t, &ss.ichHits), otherSite(t, &ss.otherHit)
	ich, _ := url.Parse(ss.ich.URL)
	other, _ := url.Parse(ss.other.URL)
	fetcher := ai.NewFetcher().AllowForTest(func(netip.AddrPort) bool { return true }, 5*time.Second)
	s.discover = discover.New(fetcher, []discover.Site{discover.NewIchkoche(ich, ich.Host), listSite{"kochbar.test", other}}, nil)
	return ss
}

func (ss *sites) searches() int32 { return ss.ichHits.Load() + ss.otherHit.Load() }

// inOrder reports whether body has each of want, in that order.
func inOrder(body string, want ...string) bool {
	at := 0
	for _, w := range want {
		i := strings.Index(body[at:], w)
		if i < 0 {
			return false
		}
		at += i + len(w)
	}
	return true
}

// TestWebSearch searches the recipe sites from the search page without
// JavaScript: merged hits, site chips, "More results", a site that is
// down and a query too short to search.
func TestWebSearch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startImportServer(t, db)
		ss := withSites(t, s)
		anna, _ := household(t, srv)
		createRecipe(t, anna, stickyTofu())
		tofu, curry := ss.ich.URL+"/crispy-tofu-rezept-1", ss.other.URL+"/r/tofu-curry"

		// The import tab no longer links to a search page of its own.
		if body := anna.get("/recipes/import").body; strings.Contains(body, ui.DiscoverPath) {
			t.Errorf("import form links to the site search:\n%.3000s", body)
		}
		anna.get(ui.DiscoverPath).expect(t, "old search page", http.StatusNotFound)

		// Without a query nothing is searched.
		if body := anna.get("/recipes/search").body; strings.Contains(body, "From the web") || ss.searches() != 0 {
			t.Errorf("empty search: %d searches\n%.3000s", ss.searches(), body)
		}

		body := anna.get("/recipes/search?q=tofu").expect(t, "search", http.StatusOK).body
		photo := ui.DiscoverPhotoHref(ss.ich.URL + "/photo.png")
		for _, want := range []string{
			// The library's hits come first, then the sites'.
			"Sticky <mark>tofu</mark>", "From the web", "Not in your recipes yet",
			`aria-current="true" data-on:click__prevent="$site = &#34;&#34;; $wpages = 1">All sites</a>`, `href="/recipes/search?q=tofu&amp;site=kochbar.test"`,
			"★ 5/5 · 12 ratings · Eier-Koch", html.EscapeString(photo), `<span class="web-hit__site">kochbar.test</span>`,
			`name="url" value="` + tofu + `"`, `aria-label="Import Crispy tofu"`,
			`href="` + html.EscapeString(ui.DiscoverPreviewHref(tofu, ui.WebSearch{Query: "tofu", Pages: 1})) + `"`,
			`href="/recipes/search?q=tofu&amp;wp=2"`, "More results", "page=search-web",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("search lacks %q", want)
			}
		}
		// Each page's hits alternate between the sites.
		if !inOrder(body, "Sticky <mark>tofu</mark>", "From the web", "Crispy tofu", "Tofu curry", "Tofu scramble") {
			t.Error("hits out of order")
		}
		if t.Failed() {
			t.Fatalf("search:\n%.8000s", body)
		}
		if res := anna.get(photo); res.status != http.StatusOK || !strings.HasPrefix(res.body, "\x89PNG") {
			t.Errorf("photo: %d %.20q", res.status, res.body)
		}
		anna.get("/recipes/discover/photo?u="+url.QueryEscape("https://evil.example/x.png")).expect(t, "photo from elsewhere", http.StatusNotFound)

		// "More results" appends the next page, from the cache for the
		// first; the last page has no "More results".
		before := ss.searches()
		body = anna.get("/recipes/search?q=tofu&wp=2").body
		if !inOrder(body, "Crispy tofu", "Tofu curry", "Tofu scramble", "Tofu stew") || strings.Contains(body, "More results") {
			t.Errorf("two pages:\n%.8000s", body)
		}
		if got := ss.searches() - before; got != 1 {
			t.Errorf("two pages: %d searches, want 1", got)
		}

		// A chip searches its site alone.
		body = anna.get("/recipes/search?q=tofu&site=kochbar.test").body
		if !strings.Contains(body, `aria-current="true" data-on:click__prevent="$site = &#34;kochbar.test&#34;; $wpages = 1">kochbar.test</a>`) || !strings.Contains(body, "Tofu curry") ||
			strings.Contains(body, "Crispy tofu") || !strings.Contains(body, "More from kochbar.test") ||
			!strings.Contains(body, `href="/recipes/search?q=tofu&amp;site=kochbar.test&amp;wp=2"`) {
			t.Errorf("one site:\n%.8000s", body)
		}
		// An unknown site searches all of them.
		if body := anna.get("/recipes/search?q=tofu&site=evil.example").body; !strings.Contains(body, "Crispy tofu") || !strings.Contains(body, "Tofu curry") {
			t.Errorf("unknown site:\n%.8000s", body)
		}

		// A site that fails is marked; the other's hits still show.
		body = anna.get("/recipes/search?q=boom").expect(t, "site down", http.StatusOK).body
		for _, want := range []string{"Boom bowl", `<span class="chip chip--dashed chip--down" aria-disabled="true">ichkoche.at <span class="chip__count">unavailable</span>`,
			"Couldn’t search ichkoche.at right now"} {
			if !strings.Contains(body, want) {
				t.Errorf("site down lacks %q", want)
			}
		}
		if t.Failed() {
			t.Fatalf("site down:\n%.8000s", body)
		}

		// Short queries search the library only.
		before = ss.searches()
		body = anna.get("/recipes/search?q=to").body
		if !strings.Contains(body, "Sticky <mark>to</mark>fu") || !strings.Contains(body, "Type at least 3 letters") || ss.searches() != before {
			t.Errorf("short query: %d searches\n%.6000s", ss.searches()-before, body)
		}

		if body := anna.get("/recipes/search?q=nothing").body; !strings.Contains(body, "Nothing found on recipe sites for “nothing”") {
			t.Errorf("no results:\n%.6000s", body)
		}

		// Previewing a hit names its own site, and leads back to the search.
		body = anna.get(ui.DiscoverPreviewHref(curry, ui.WebSearch{Query: "tofu", Site: "kochbar.test", Pages: 2})).expect(t, "preview", http.StatusOK).body
		if !strings.Contains(body, "Preview from kochbar.test") || !strings.Contains(body, `href="/recipes/search?q=tofu&amp;site=kochbar.test&amp;wp=2"`) ||
			!strings.Contains(body, `name="site" value="kochbar.test"`) {
			t.Errorf("preview:\n%.6000s", body)
		}
	})
}

// TestWebSearchImport imports hits from the search page with and without
// JavaScript, follows them on the live stream and to the library, and
// keeps each member's imports their own.
func TestWebSearchImport(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startImportServer(t, db)
		ss := withSites(t, s)
		anna, ben := household(t, srv)
		tofu, scramble := ss.ich.URL+"/crispy-tofu-rezept-1", ss.ich.URL+"/tofu-scramble-rezept-2"
		results := "/recipes/search?q=tofu"

		// Without JavaScript the import goes back to the search, which
		// follows it until the draft is ready.
		anna.post("/recipes/discover/import", url.Values{"url": {tofu}, "q": {"tofu"}, "wp": {"1"}}).
			expect(t, "import", http.StatusSeeOther, results)
		body := waitForPage(t, anna, results, "Ready to review")
		m := discoverJobRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no import link:\n%.6000s", body)
		}
		// Imports are the importer's own: the other member can still import it.
		if other := ben.get(results).body; strings.Contains(other, "Ready to review") || !strings.Contains(other, `value="`+tofu+`"`) {
			t.Errorf("other member sees the import:\n%.6000s", other)
		}

		// Once saved the hit is in the household's recipes, for everyone.
		saved := anna.post(m[1]+"/save", nil).expect(t, "save", http.StatusSeeOther)
		for _, b := range []*browser{anna, ben} {
			if got := b.get(results).body; !strings.Contains(got, "In your recipes") || !strings.Contains(got, `href="`+saved.location+`"`) {
				t.Errorf("saved hit:\n%.6000s", got)
			}
		}

		// With JavaScript the section is patched in place, and the live
		// stream follows the import. The search is cached: no placeholders.
		live := anna.openLive("page=search-web&datastar=" + url.QueryEscape(`{"wq":"tofu","site":"","wpages":1}`))
		first := live.next("first render", 5*time.Second, `id="search-web"`)
		if strings.Contains(first, "web-hit--skeleton") || !strings.Contains(first, "Tofu curry") {
			t.Errorf("first render:\n%.6000s", first)
		}
		res := anna.datastar(http.MethodPost, "/recipes/discover/import", url.Values{"url": {scramble}, "q": {"tofu"}, "wp": {"1"}})
		if res.status != http.StatusOK || !strings.Contains(res.body, `id="search-web"`) || strings.Contains(res.body, `value="`+scramble+`"`) {
			t.Errorf("datastar import: %d\n%.3000s", res.status, res.body)
		}
		live.next("ready", 10*time.Second, `id="search-web"`, "Ready to review")

		// A bad link shows the error in the section.
		res = anna.post("/recipes/discover/import", url.Values{"url": {"ftp://x"}, "q": {"tofu"}})
		if res.status != http.StatusUnprocessableEntity || !strings.Contains(res.body, "Enter a web address") || !strings.Contains(res.body, "Tofu curry") {
			t.Errorf("bad link: %d\n%.3000s", res.status, res.body)
		}
		res = anna.datastar(http.MethodPost, "/recipes/discover/import", url.Values{"url": {"ftp://x"}, "q": {"tofu"}})
		if res.status != http.StatusOK || !strings.Contains(res.body, `id="search-web"`) || !strings.Contains(res.body, "Enter a web address") {
			t.Errorf("datastar bad link: %d\n%.3000s", res.status, res.body)
		}

		// A search that is not cached shows placeholders while the sites
		// are asked, then the results; after "More results" the shown page
		// stays above the placeholder.
		live = anna.openLive("page=search-web&datastar=" + url.QueryEscape(`{"wq":"nothing","site":"","wpages":1}`))
		live.next("placeholders", 5*time.Second, `id="search-web"`, "web-hit--skeleton", `aria-busy="true"`)
		live.next("results", 5*time.Second, `id="search-web"`, "Nothing found on recipe sites")
		live = anna.openLive("page=search-web&datastar=" + url.QueryEscape(`{"wq":"tofu","site":"","wpages":2}`))
		more := live.next("more", 5*time.Second, `id="search-web"`, "web-hit--skeleton")
		if !strings.Contains(more, "Tofu curry") || strings.Contains(more, "Tofu stew") {
			t.Errorf("loading more:\n%.6000s", more)
		}
		live.next("second page", 5*time.Second, `id="search-web"`, "Tofu stew")

		// A short query asks no site.
		before := ss.searches()
		live = anna.openLive("page=search-web&datastar=" + url.QueryEscape(`{"wq":"to","site":"","wpages":1}`))
		live.next("short", 5*time.Second, `id="search-web"`, "Type at least 3 letters")
		if ss.searches() != before {
			t.Errorf("short query on the stream: %d searches", ss.searches()-before)
		}
	})
}

// TestDiscoverPreview opens a hit as it would be imported, imports it from
// there with and without JavaScript, and previews nothing but the sites'
// recipe pages.
func TestDiscoverPreview(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startImportServer(t, db)
		ss := withSites(t, s)
		site := ss.ich
		anna, _ := household(t, srv)
		tofu, scramble := site.URL+"/crispy-tofu-rezept-1", site.URL+"/tofu-scramble-rezept-2"
		ws := ui.WebSearch{Query: "tofu", Pages: 2}
		preview := func(link string) string { return ui.DiscoverPreviewHref(link, ws) }

		body := anna.get(preview(tofu)).expect(t, "preview", http.StatusOK).body
		for _, want := range []string{
			"<title>Crispy tofu · mise</title>", "Preview from ichkoche.at, not in your recipes yet",
			"400 g", "Fry the", "25 min", html.EscapeString(ui.DiscoverPhotoHref(site.URL + "/photo.png")),
			`name="from" value="preview"`, `name="wp" value="2"`, `name="url" value="` + tofu + `"`,
			// Back to the search it came from; the site only as a secondary link.
			`href="/recipes/search?q=tofu&amp;wp=2"`, "Back to search", `href="` + tofu + `"`, "page=discover-preview",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("preview lacks %q", want)
			}
		}
		if t.Failed() {
			t.Fatalf("preview:\n%.8000s", body)
		}

		// Only the sites' recipe pages are previewed.
		for _, link := range []string{"https://evil.example/crispy-tofu-rezept-1", site.URL + "/kochbuch/oma/tofu-7", ss.other.URL + "/search", "", "not a url"} {
			anna.get(ui.DiscoverPreviewHref(link, ui.WebSearch{})).expect(t, "preview of "+link, http.StatusNotFound)
		}
		anna.get("/live?page=discover-preview&u="+url.QueryEscape("https://evil.example/x-rezept-1")).expect(t, "live of off-site page", http.StatusNotFound)

		if body := anna.get(preview(site.URL + "/plain-rezept-3")).body; !strings.Contains(body, "No recipe data on this page") ||
			!strings.Contains(body, `name="from" value="preview"`) {
			t.Errorf("page without recipe data:\n%.4000s", body)
		}
		if body := anna.get(preview(site.URL + "/missing-rezept-4")).body; !strings.Contains(body, "couldn’t be reached") {
			t.Errorf("unreachable page:\n%.4000s", body)
		}

		// Without JavaScript the import comes back to the preview, which
		// follows it.
		anna.post("/recipes/discover/import", url.Values{"url": {tofu}, "q": {"tofu"}, "wp": {"2"}, "from": {"preview"}}).
			expect(t, "import from preview", http.StatusSeeOther, preview(tofu))
		body = waitForPage(t, anna, preview(tofu), "Ready to review")
		if strings.Contains(body, `name="url" value="`+tofu+`"`) {
			t.Errorf("preview still offers the import:\n%.4000s", body)
		}

		// With JavaScript the import state is patched in place and the live
		// stream follows it.
		live := anna.openLive("page=discover-preview&q=tofu&wp=2&u=" + url.QueryEscape(scramble))
		live.next("first render", 5*time.Second, "discover-preview-action")
		res := anna.datastar(http.MethodPost, "/recipes/discover/import", url.Values{"url": {scramble}, "q": {"tofu"}, "wp": {"2"}, "from": {"preview"}})
		if res.status != http.StatusOK || !strings.Contains(res.body, "discover-preview-action") || strings.Contains(res.body, `value="`+scramble+`"`) {
			t.Errorf("datastar import: %d\n%.3000s", res.status, res.body)
		}
		live.next("ready", 10*time.Second, "discover-preview-action", "Ready to review")
	})
}
