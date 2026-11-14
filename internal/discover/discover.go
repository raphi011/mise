// Package discover searches recipe sites through their own search, so
// recipes can be found and imported without leaving mise. Each supported
// site is a Site that knows its search address and how to read its result
// page; a Service searches all of them at once and merges their hits.
// Importing a hit is an ordinary link import.
package discover

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
)

// Errors.
var (
	// ErrUnavailable: a site's search could not be fetched or read.
	ErrUnavailable = errors.New("discover: site search unavailable")
	// ErrPhotoHost: a photo is not on a site's photo host.
	ErrPhotoHost = errors.New("discover: photo not from a site")
)

// Limits on what a search asks of the sites.
const (
	// MinQuery is the shortest query searched, in characters: shorter ones
	// match too much to be useful and would hit the sites per keystroke.
	MinQuery = 3
	// MaxQuery is the longest query passed on, in characters.
	MaxQuery = 100
	// MaxPages is how many result pages can be asked for.
	MaxPages = 50
)

// Result pages are cached briefly: "More results", the live stream
// re-rendering after an import and a reload must not fetch a site again.
// A failed search is cached for a shorter time, so a site that is down is
// not asked again on every re-render.
const (
	cacheTTL  = 10 * time.Minute
	failTTL   = time.Minute
	cacheSize = 200
)

// Site is a recipe site whose own search mise uses.
type Site interface {
	// Name is the site's name as people know it ("ichkoche.at"). It also
	// keys the site in households' settings, so it must not change.
	Name() string
	// Language is the language the site's recipes are written in.
	Language() language.Tag
	// SearchURL is the result page for q, page counting from 1, and what
	// it is (a page or JSON).
	SearchURL(q string, page int) (string, ai.Kind)
	// ParseResults reads result page page; f.URL is its final URL.
	ParseResults(f ai.Fetched, page int) (ResultPage, error)
	// RecipeURL reports whether u is one of the site's recipe pages, and
	// returns it without query or fragment.
	RecipeURL(u *url.URL) (string, bool)
	// PhotoHost reports whether the site's result photos come from host,
	// a URL's host with its port, if any (url.URL.Host). Photo fetches
	// nothing else, so it cannot serve as an open proxy.
	PhotoHost(host string) bool
}

// ResultPage is one page of a site's search results.
type ResultPage struct {
	Hits []Hit
	// More reports whether a next page exists.
	More bool
}

// Hit is one recipe in a site's search results.
type Hit struct {
	// Site is the name of the site the hit is from.
	Site string
	// URL is the recipe page, absolute.
	URL   string
	Title string
	// Photo is the result photo's URL, or empty.
	Photo string
	// Snippet is the site's short text about the recipe, or empty.
	Snippet string
	// Stars is the rating in whole stars, 0 to 5; 0 means none.
	Stars int
	// Ratings is how many ratings the stars are from.
	Ratings int
	// Level is the site's difficulty label, in its language.
	Level string
}

// Results are the first pages of a search, merged across the searched
// sites.
type Results struct {
	Hits []Hit
	// More reports whether a searched site has a next page.
	More bool
	// Unavailable names the searched sites that could not be searched, in
	// site order. Their hits are missing; the others' are not.
	Unavailable []string
	// Err joins the failures of this search's own fetches (not those
	// remembered from an earlier one), for the log; nil when there were
	// none.
	Err error
}

// Fetcher fetches pages and photos from the public internet.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, kind ai.Kind) (ai.Fetched, error)
	// FetchOnHost is Fetch that fails (ai.ErrOtherHost) when rawURL or a
	// redirect is on another host.
	FetchOnHost(ctx context.Context, rawURL, host string, kind ai.Kind) (ai.Fetched, error)
}

// Service searches a set of sites.
type Service struct {
	fetcher Fetcher
	sites   []Site
	now     func() time.Time

	mu    sync.Mutex
	cache map[cacheKey]cached
}

type cacheKey struct {
	site string
	q    string
	page int
}

type cached struct {
	page   ResultPage
	failed bool
	at     time.Time
}

// New returns a Service searching sites, in that order. now defaults to
// time.Now.
func New(f Fetcher, sites []Site, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{fetcher: f, sites: sites, now: now, cache: map[cacheKey]cached{}}
}

// Sites are the sites, in order.
func (s *Service) Sites() []Site { return slices.Clone(s.sites) }

// HasSite reports whether name is one of the searched sites.
func (s *Service) HasSite(name string) bool {
	for _, st := range s.sites {
		if st.Name() == name {
			return true
		}
	}
	return false
}

// Query trims q and cuts it to MaxQuery characters.
func Query(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if utf8.RuneCountInString(q) > MaxQuery {
		q = string([]rune(q)[:MaxQuery])
	}
	return q
}

// Searchable reports whether q (normalised with Query) is long enough to
// be searched.
func Searchable(q string) bool { return utf8.RuneCountInString(Query(q)) >= MinQuery }

// Pages clamps a page count to 1..MaxPages.
func Pages(n int) int { return min(max(n, 1), MaxPages) }

// Search returns the first pages result pages for q (normalised with Query
// and Pages) from the named sites, merged. The sites are searched at once;
// each page's hits are interleaved in site order, so no site's hits bury
// another's. A site that fails is named in Unavailable. Unknown names are
// ignored; a query that is not Searchable, or no known site, has no
// results and fetches nothing.
func (s *Service) Search(ctx context.Context, q string, names []string, pages int) Results {
	q, pages = Query(q), Pages(pages)
	sites := s.searched(names)
	if !Searchable(q) || len(sites) == 0 {
		return Results{}
	}
	walks := make([]walk, len(sites))
	var wg sync.WaitGroup
	for i, st := range sites {
		wg.Go(func() { walks[i], _ = s.walk(ctx, st, q, pages, true) })
	}
	wg.Wait()
	return merge(sites, walks, pages)
}

// Cached reports whether Search(q, names, pages) would be answered from
// the cache alone, without fetching anything.
func (s *Service) Cached(q string, names []string, pages int) bool {
	q, pages = Query(q), Pages(pages)
	if !Searchable(q) {
		return true
	}
	for _, st := range s.searched(names) {
		if _, complete := s.walk(context.Background(), st, q, pages, false); !complete {
			return false
		}
	}
	return true
}

// searched are the named sites, in site order.
func (s *Service) searched(names []string) []Site {
	var out []Site
	for _, st := range s.sites {
		if slices.Contains(names, st.Name()) {
			out = append(out, st)
		}
	}
	return out
}

// walk is one site's part of a search.
type walk struct {
	pages  []ResultPage
	more   bool
	failed bool
	err    error
}

// walk reads site's result pages 1..pages for q, stopping at the site's
// last page or a failure. Without fetch it only reads the cache, and
// complete reports whether the cache held every page needed.
func (s *Service) walk(ctx context.Context, site Site, q string, pages int, fetch bool) (w walk, complete bool) {
	for p := 1; p <= pages; p++ {
		key := cacheKey{site.Name(), strings.ToLower(q), p}
		c, ok := s.cached(key)
		if !ok {
			if !fetch {
				return w, false
			}
			var err error
			c, err = s.fetch(ctx, site, key, q)
			if err != nil {
				w.err = fmt.Errorf("%s: %w", site.Name(), err)
			}
		}
		if c.failed {
			w.failed = true
			return w, true
		}
		w.pages = append(w.pages, c.page)
		if !c.page.More {
			return w, true
		}
	}
	w.more = true
	return w, true
}

// fetch reads one result page from site and caches it, or caches that it
// failed. A failure because ctx ended (the viewer moved on) is not the
// site's, and is not cached.
func (s *Service) fetch(ctx context.Context, site Site, key cacheKey, q string) (cached, error) {
	page, err := s.read(ctx, site, q, key.page)
	if err != nil {
		if ctx.Err() == nil {
			s.store(key, cached{failed: true})
		}
		return cached{failed: true}, err
	}
	for i := range page.Hits {
		page.Hits[i].Site = site.Name()
	}
	c := cached{page: page}
	s.store(key, c)
	return c, nil
}

func (s *Service) read(ctx context.Context, site Site, q string, page int) (ResultPage, error) {
	u, kind := site.SearchURL(q, page)
	f, err := s.fetcher.Fetch(ctx, u, kind)
	if err != nil {
		return ResultPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	res, err := site.ParseResults(f, page)
	if err != nil {
		return ResultPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return res, nil
}

// merge interleaves the sites' hits page by page, dropping repeats.
func merge(sites []Site, walks []walk, pages int) Results {
	var res Results
	var errs []error
	seen := map[string]bool{}
	for p := range pages {
		longest := 0
		for _, w := range walks {
			if p < len(w.pages) {
				longest = max(longest, len(w.pages[p].Hits))
			}
		}
		for i := range longest {
			for _, w := range walks {
				if p >= len(w.pages) || i >= len(w.pages[p].Hits) {
					continue
				}
				h := w.pages[p].Hits[i]
				if !seen[h.URL] {
					seen[h.URL] = true
					res.Hits = append(res.Hits, h)
				}
			}
		}
	}
	for i, w := range walks {
		res.More = res.More || w.more
		if w.failed {
			res.Unavailable = append(res.Unavailable, sites[i].Name())
		}
		if w.err != nil {
			errs = append(errs, w.err)
		}
	}
	res.Err = errors.Join(errs...)
	return res
}

// site is the site rawURL is on, judged by match; ok is false for
// anything but an http(s) address one of the sites accepts.
func (s *Service) site(rawURL string, match func(Site, *url.URL) bool) (Site, *url.URL, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, nil, false
	}
	for _, st := range s.sites {
		if match(st, u) {
			return st, u, true
		}
	}
	return nil, nil, false
}

// RecipeURL reports whether rawURL is a recipe page of one of the sites,
// and returns it in the form search results use, with the site's name.
// Only such pages are previewed, so the preview cannot fetch arbitrary
// pages.
func (s *Service) RecipeURL(rawURL string) (link, site string, ok bool) {
	st, u, ok := s.site(rawURL, func(st Site, u *url.URL) bool {
		_, ok := st.RecipeURL(u)
		return ok
	})
	if !ok {
		return "", "", false
	}
	link, _ = st.RecipeURL(u)
	return link, st.Name(), true
}

// IsPhoto reports whether rawURL is on one of the sites' photo hosts, the
// only photos Photo serves.
func (s *Service) IsPhoto(rawURL string) bool {
	_, _, ok := s.photo(rawURL)
	return ok
}

func (s *Service) photo(rawURL string) (Site, *url.URL, bool) {
	return s.site(rawURL, func(st Site, u *url.URL) bool { return st.PhotoHost(u.Host) })
}

// Photo fetches a result or recipe photo, which must be on one of the
// sites' photo hosts, redirects included.
func (s *Service) Photo(ctx context.Context, rawURL string) (ai.Fetched, error) {
	_, u, ok := s.photo(rawURL)
	if !ok {
		return ai.Fetched{}, ErrPhotoHost
	}
	return s.fetcher.FetchOnHost(ctx, rawURL, u.Hostname(), ai.KindImage)
}

func (s *Service) cached(key cacheKey) (cached, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cache[key]
	if !ok || s.now().Sub(c.at) > c.ttl() {
		return cached{}, false
	}
	return c, true
}

func (c cached) ttl() time.Duration {
	if c.failed {
		return failTTL
	}
	return cacheTTL
}

// store caches c, dropping expired entries and then the oldest when the
// cache is full.
func (s *Service) store(key cacheKey, c cached) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.cache) >= cacheSize {
		var oldest cacheKey
		var oldestAt time.Time
		for k, e := range s.cache {
			if now.Sub(e.at) > e.ttl() {
				delete(s.cache, k)
				continue
			}
			if oldestAt.IsZero() || e.at.Before(oldestAt) {
				oldest, oldestAt = k, e.at
			}
		}
		if len(s.cache) >= cacheSize {
			delete(s.cache, oldest)
		}
	}
	c.at = now
	s.cache[key] = c
}
