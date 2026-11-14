package discover_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/discover"
)

// fakeFetcher serves fixed bodies by URL and records what was fetched,
// and the hosts fetches were pinned to.
type fakeFetcher struct {
	mu      sync.Mutex
	pages   map[string]string
	fetched []string
	pinned  []string
}

func (f *fakeFetcher) FetchOnHost(ctx context.Context, rawURL, host string, kind ai.Kind) (ai.Fetched, error) {
	f.mu.Lock()
	f.pinned = append(f.pinned, host)
	f.mu.Unlock()
	return f.Fetch(ctx, rawURL, kind)
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL string, kind ai.Kind) (ai.Fetched, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, rawURL)
	if err := ctx.Err(); err != nil {
		return ai.Fetched{}, err
	}
	body, ok := f.pages[rawURL]
	if !ok {
		return ai.Fetched{}, &ai.StatusError{Code: 404}
	}
	u, _ := url.Parse(rawURL)
	ct := "text/html; charset=utf-8"
	switch kind {
	case ai.KindImage:
		ct = "image/jpeg"
	case ai.KindJSON:
		ct = "application/json; charset=UTF-8"
	}
	return ai.Fetched{URL: u, ContentType: ct, Body: []byte(body), Header: http.Header{}}, nil
}

func (f *fakeFetcher) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.fetched)
	slices.Sort(out)
	return out
}

func (f *fakeFetcher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = nil
}

// listSite is a made-up site: its result page is a list of
// <a class="hit"> links, and <a rel="next"> when there is a next page.
type listSite struct{ name, host string }

func (s listSite) Name() string { return s.name }

func (listSite) Language() language.Tag { return language.English }

func (s listSite) SearchURL(q string, page int) (string, ai.Kind) {
	return s.search(q, page), ai.KindPage
}

func (s listSite) search(q string, page int) string {
	return "https://" + s.host + "/search?" + url.Values{"q": {q}, "p": {strconv.Itoa(page)}}.Encode()
}

func (s listSite) ParseResults(f ai.Fetched, _ int) (discover.ResultPage, error) {
	doc, err := html.Parse(bytes.NewReader(f.Body))
	if err != nil {
		return discover.ResultPage{}, err
	}
	base := f.URL
	var res discover.ResultPage
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			var href, class, rel string
			for _, a := range n.Attr {
				switch a.Key {
				case "href":
					href = a.Val
				case "class":
					class = a.Val
				case "rel":
					rel = a.Val
				}
			}
			if rel == "next" {
				res.More = true
			}
			if u, err := base.Parse(href); err == nil && class == "hit" && n.FirstChild != nil {
				res.Hits = append(res.Hits, discover.Hit{URL: u.String(), Title: n.FirstChild.Data})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return res, nil
}

func (s listSite) RecipeURL(u *url.URL) (string, bool) {
	if u.Host != s.host || !strings.HasPrefix(u.Path, "/r/") {
		return "", false
	}
	return "https://" + s.host + u.Path, true
}

func (s listSite) PhotoHost(host string) bool { return host == "img."+s.host }

// results is a listSite result page with hits named by their path.
func results(more bool, paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(`<a class="hit" href="/r/` + p + `">` + p + `</a>`)
	}
	if more {
		b.WriteString(`<a rel="next" href="?p=next">next</a>`)
	}
	return b.String()
}

func urls(hits []discover.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Site + " " + strings.TrimPrefix(h.URL, "https://"+h.Site)
	}
	return out
}

func fixture(t *testing.T) string {
	t.Helper()
	return readFixture(t, "ichkoche_results.html")
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const searchURL = "https://www.ichkoche.at/suche/rezepte?q=kn%C3%B6del"

func TestIchkocheSearchURL(t *testing.T) {
	site := discover.Ichkoche()
	if got, kind := site.SearchURL("knödel", 1); got != searchURL || kind != ai.KindPage {
		t.Errorf("page 1: %s", got)
	}
	if got, _ := site.SearchURL("a&b", 3); got != "https://www.ichkoche.at/suche/rezepte?page=3&q=a%26b" {
		t.Errorf("page 3: %s", got)
	}
}

func TestIchkocheResults(t *testing.T) {
	f := &fakeFetcher{pages: map[string]string{searchURL: fixture(t)}}
	s := discover.New(f, []discover.Site{discover.Ichkoche()}, nil)
	res := s.Search(t.Context(), "knödel", []string{"ichkoche.at"}, 1)
	// The article teaser, the member's cookbook entry, the repeated recipe
	// and the link to another site are left out.
	want := []discover.Hit{
		{
			Site: "ichkoche.at", URL: "https://www.ichkoche.at/semmelknoedel-rezept-101", Title: "Semmelknödel",
			Photo:   "https://assets.ichkoche.at/image/1/101.jpg?v=1&twic=v1/cover=360x280",
			Snippet: "Für die Semmelknödel die Semmeln würfelig schneiden und mit Milch übergießen.",
			Stars:   5, Ratings: 1204, Level: "Eier-Koch",
		},
		// The query is dropped from the link; a photo from another host is
		// not used.
		{Site: "ichkoche.at", URL: "https://www.ichkoche.at/marillenknoedel-rezept-202", Title: "Marillenknödel", Stars: 4, Ratings: 1, Level: "Hobby-Koch"},
	}
	if !slices.Equal(res.Hits, want) {
		t.Errorf("hits:\n got %+v\nwant %+v", res.Hits, want)
	}
	if !res.More || res.Unavailable != nil || res.Err != nil {
		t.Errorf("more %v, unavailable %q, err %v", res.More, res.Unavailable, res.Err)
	}
}

// TestSearchMerge searches two sites at once: each page's hits are
// interleaved in site order, "More" pages are appended, and a site that
// has no more pages is not asked again.
func TestSearchMerge(t *testing.T) {
	a, b := listSite{"a.test", "a.test"}, listSite{"b.test", "b.test"}
	f := &fakeFetcher{pages: map[string]string{
		a.search("tofu", 1): results(true, "a1", "a2", "a3"),
		a.search("tofu", 2): results(false, "a4", "a1"),
		b.search("tofu", 1): results(false, "b1"),
	}}
	s := discover.New(f, []discover.Site{a, b}, nil)
	ctx := t.Context()
	both := []string{"a.test", "b.test"}

	if got := s.Sites(); len(got) != 2 || got[0].Name() != "a.test" || !s.HasSite("b.test") || s.HasSite("c.test") {
		t.Errorf("sites %v", got)
	}

	res := s.Search(ctx, "tofu", both, 1)
	if got, want := urls(res.Hits), []string{"a.test /r/a1", "b.test /r/b1", "a.test /r/a2", "a.test /r/a3"}; !slices.Equal(got, want) {
		t.Errorf("page 1: %q, want %q", got, want)
	}
	if !res.More || res.Unavailable != nil {
		t.Errorf("page 1: more %v, unavailable %q", res.More, res.Unavailable)
	}

	// The second page is appended; a1, already shown, is not repeated, and
	// b.test, which had no second page, is not asked for one.
	f.reset()
	res = s.Search(ctx, "tofu", both, 2)
	if got, want := urls(res.Hits), []string{"a.test /r/a1", "b.test /r/b1", "a.test /r/a2", "a.test /r/a3", "a.test /r/a4"}; !slices.Equal(got, want) {
		t.Errorf("pages 1-2: %q, want %q", got, want)
	}
	if res.More {
		t.Error("pages 1-2: more")
	}
	if got := f.took(); !slices.Equal(got, []string{a.search("tofu", 2)}) {
		t.Errorf("pages 1-2 fetched %q", got)
	}

	// Naming one site searches it alone; an unknown site nothing.
	res = s.Search(ctx, "tofu", []string{"b.test"}, 3)
	if got := urls(res.Hits); !slices.Equal(got, []string{"b.test /r/b1"}) || res.More {
		t.Errorf("b.test only: %q, more %v", got, res.More)
	}
	f.reset()
	if res := s.Search(ctx, "tofu", []string{"c.test"}, 1); len(res.Hits) != 0 || len(f.took()) != 0 {
		t.Errorf("unknown site: %+v, fetched %q", res, f.took())
	}
}

// TestSearchUnavailable: a site that fails is named, the others' hits
// still show, and the failure is remembered for a minute.
func TestSearchUnavailable(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	a, down := listSite{"a.test", "a.test"}, listSite{"down.test", "down.test"}
	f := &fakeFetcher{pages: map[string]string{a.search("tofu", 1): results(false, "a1")}}
	s := discover.New(f, []discover.Site{down, a}, func() time.Time { return now })
	ctx := t.Context()
	both := []string{"a.test", "down.test"}

	if s.Cached("tofu", both, 1) {
		t.Error("cached before the first search")
	}
	res := s.Search(ctx, "tofu", both, 1)
	if got := urls(res.Hits); !slices.Equal(got, []string{"a.test /r/a1"}) ||
		!slices.Equal(res.Unavailable, []string{"down.test"}) || !errors.Is(res.Err, discover.ErrUnavailable) ||
		!strings.Contains(res.Err.Error(), "down.test") {
		t.Errorf("hits %q, unavailable %q, err %v", got, res.Unavailable, res.Err)
	}
	if !s.Cached("tofu", both, 1) {
		t.Error("not cached after the search")
	}

	// Within the minute the failure is not fetched again, nor logged again.
	f.reset()
	res = s.Search(ctx, "tofu", both, 1)
	if len(f.took()) != 0 || !slices.Equal(res.Unavailable, []string{"down.test"}) || res.Err != nil {
		t.Errorf("remembered failure: fetched %q, unavailable %q, err %v", f.took(), res.Unavailable, res.Err)
	}
	now = now.Add(61 * time.Second)
	if s.Cached("tofu", both, 1) {
		t.Error("failure still cached after a minute")
	}
	s.Search(ctx, "tofu", both, 1)
	if got := f.took(); !slices.Equal(got, []string{down.search("tofu", 1)}) {
		t.Errorf("after a minute fetched %q", got)
	}

	// A search the viewer gave up on is not the site's failure.
	f.pages[down.search("soup", 1)] = results(false, "d1")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	s.Search(canceled, "soup", []string{"down.test"}, 1)
	if s.Cached("soup", []string{"down.test"}, 1) {
		t.Error("canceled search cached")
	}
	if got := urls(s.Search(ctx, "soup", []string{"down.test"}, 1).Hits); !slices.Equal(got, []string{"down.test /r/d1"}) {
		t.Errorf("after cancel: %q", got)
	}
}

func TestSearchCacheAndLimits(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	f := &fakeFetcher{pages: map[string]string{searchURL: fixture(t)}}
	s := discover.New(f, []discover.Site{discover.Ichkoche()}, func() time.Time { return now })
	ctx := t.Context()
	ich := []string{"ichkoche.at"}

	// Short queries are not searched.
	for _, q := range []string{"", "   ", "kn", " k  "} {
		if res := s.Search(ctx, q, ich, 1); len(res.Hits) != 0 || len(f.took()) != 0 || !s.Cached(q, ich, 1) {
			t.Errorf("query %q: %+v, fetched %q", q, res, f.took())
		}
	}
	// The same search differing in case and spacing is served from the
	// cache until it expires.
	for _, q := range []string{"knödel", "  Knödel "} {
		s.Search(ctx, q, ich, 1)
	}
	if len(f.took()) != 1 {
		t.Errorf("fetched %q, want one fetch", f.took())
	}
	now = now.Add(11 * time.Minute)
	if s.Search(ctx, "knödel", ich, 1); len(f.took()) != 2 {
		t.Errorf("after expiry fetched %q", f.took())
	}

	// Queries are cut short, page counts clamped.
	f.reset()
	s.Search(ctx, strings.Repeat("x", 300), ich, 1)
	if got := f.took(); len(got) != 1 || !strings.Contains(got[0], "q="+strings.Repeat("x", discover.MaxQuery)) ||
		strings.Contains(got[0], strings.Repeat("x", discover.MaxQuery+1)) {
		t.Errorf("long query fetched %q", got)
	}
	if discover.Pages(-3) != 1 || discover.Pages(999) != discover.MaxPages || discover.Query(" a \n b ") != "a b" ||
		discover.Searchable("ab") || !discover.Searchable("äöü") {
		t.Error("normalising")
	}
}

func TestPhoto(t *testing.T) {
	photo, other := "https://assets.ichkoche.at/image/1/101.jpg", "https://img.b.test/1.jpg"
	f := &fakeFetcher{pages: map[string]string{photo: "jpeg", other: "other"}}
	s := discover.New(f, []discover.Site{discover.Ichkoche(), listSite{"b.test", "b.test"}}, nil)
	for u, want := range map[string]string{photo: "jpeg", other: "other"} {
		if res, err := s.Photo(t.Context(), u); err != nil || string(res.Body) != want {
			t.Errorf("photo %s: %v", u, err)
		}
	}
	// Not even another port of a photo host is fetched.
	for _, u := range []string{"https://www.ichkoche.at/x.jpg", "https://b.test/x.jpg", "https://evil.example/x.jpg", "file:///etc/passwd", "://",
		"https://assets.ichkoche.at:8080/image/1/101.jpg", "http://img.b.test:22/1.jpg"} {
		if _, err := s.Photo(t.Context(), u); !errors.Is(err, discover.ErrPhotoHost) {
			t.Errorf("%s: %v, want ErrPhotoHost", u, err)
		}
	}
	if len(f.took()) != 2 {
		t.Errorf("fetched %q", f.took())
	}
	// Redirects must stay on the photo host.
	f.mu.Lock()
	pinned := slices.Sorted(slices.Values(f.pinned))
	f.mu.Unlock()
	if !slices.Equal(pinned, []string{"assets.ichkoche.at", "img.b.test"}) {
		t.Errorf("pinned to %q", pinned)
	}
}

func TestRecipeURL(t *testing.T) {
	s := discover.New(&fakeFetcher{}, []discover.Site{discover.Ichkoche(), listSite{"b.test", "b.test"}}, nil)
	for raw, want := range map[string][2]string{
		"https://www.ichkoche.at/semmelknoedel-rezept-101":            {"https://www.ichkoche.at/semmelknoedel-rezept-101", "ichkoche.at"},
		"http://www.ichkoche.at/semmelknoedel-rezept-101?ref=x#steps": {"https://www.ichkoche.at/semmelknoedel-rezept-101", "ichkoche.at"},
		"https://WWW.ICHKOCHE.AT/semmelknoedel-rezept-101":            {"https://www.ichkoche.at/semmelknoedel-rezept-101", "ichkoche.at"},
		"https://www.ichkoche.at/semmelkn%6Fedel-rezept-101":          {"https://www.ichkoche.at/semmelknoedel-rezept-101", "ichkoche.at"},
		"https://www.ichkoche.at:8443/semmelknoedel-rezept-101":       {},
		"https://b.test/r/tofu?x=1":                                   {"https://b.test/r/tofu", "b.test"},
		"https://www.ichkoche.at/kochbuch/oma/knoedel-77":             {},
		"https://www.ichkoche.at/wie-gelingen-knoedel-artikel-55":     {},
		"https://b.test/search?q=tofu":                                {},
		"https://evil.example/semmelknoedel-rezept-101":               {},
		"file:///semmelknoedel-rezept-101":                            {},
		"/semmelknoedel-rezept-101":                                   {},
	} {
		link, site, ok := s.RecipeURL(raw)
		if link != want[0] || site != want[1] || ok != (want[0] != "") {
			t.Errorf("%s: %q %q %v, want %q", raw, link, site, ok, want)
		}
	}
	if !s.IsPhoto("https://assets.ichkoche.at/image/1/101.jpg") || !s.IsPhoto("https://img.b.test/x.jpg") || s.IsPhoto("https://www.ichkoche.at/x.jpg") {
		t.Error("IsPhoto")
	}
}
