package ui

import (
	"net/url"
	"strconv"
)

// DiscoverPath prefixes the recipe site endpoints: a hit's preview, its
// photo and its import. The search itself is on the recipe search page.
const DiscoverPath = "/recipes/discover"

// SearchPath is the recipe search page.
const SearchPath = "/recipes/search"

// Hit states of a recipe site search.
const (
	// DiscoverNew is a hit that can be imported.
	DiscoverNew = ""
	// DiscoverLibrary is a hit already in the library.
	DiscoverLibrary = "library"
	// DiscoverRunning, DiscoverReady and DiscoverFailed follow the
	// viewer's import of the hit.
	DiscoverRunning = "running"
	DiscoverReady   = "ready"
	DiscoverFailed  = "failed"
)

// WebSearch is the web part of a recipe search, carried through its links
// and forms: the query, the site filter ("" for all sites) and how many
// result pages are shown.
type WebSearch struct {
	Query string
	Site  string
	Pages int
}

// values are w's query parameters: q, site and wp, each left out at its
// default.
func (w WebSearch) values() url.Values {
	v := url.Values{}
	if w.Query != "" {
		v.Set("q", w.Query)
	}
	if w.Site != "" {
		v.Set("site", w.Site)
	}
	if w.Pages > 1 {
		v.Set("wp", strconv.Itoa(w.Pages))
	}
	return v
}

// SearchHref is the search page showing w.
func SearchHref(w WebSearch) string {
	if q := w.values().Encode(); q != "" {
		return SearchPath + "?" + q
	}
	return SearchPath
}

// WebView is the "From the web" section of the search page: the merged
// hits of the recipe sites for the query.
type WebView struct {
	Search WebSearch
	// Sites are the site filter chips, set only when there is more than
	// one site; Site names the only one otherwise.
	Sites []WebSite
	Site  string
	// Short, when set, is the shortest query the sites are searched for:
	// the query is shorter.
	Short int
	// Loading is set while the results are fetched; LoadingMore while
	// the next page is, below the hits shown.
	Loading     bool
	LoadingMore bool
	Hits        []DiscoverHit
	// Empty is set when the sites that answered found nothing.
	Empty bool
	// More reports whether a next page exists.
	More bool
	// Unavailable names the sites that could not be searched, joined.
	Unavailable string
	// Alert is a failed import's message.
	Alert string
	// Mixed is set when the searched sites are in more than one language,
	// so hits name theirs.
	Mixed bool
	// NoSites is set when the household searches no site; CanEnable when
	// the viewer may turn sites on.
	NoSites   bool
	CanEnable bool
}

// WebSite is a site filter chip.
type WebSite struct {
	Name string
	// Lang is the site's language code ("DE"); LangName names it.
	Lang     string
	LangName string
	// Down marks a site that could not be searched.
	Down bool
}

// DiscoverHit is one recipe in a site's results.
type DiscoverHit struct {
	// Site is the name of the site the hit is from.
	Site string
	// URL is the recipe page on the site.
	URL   string
	Title string
	// Photo is the proxied photo's address, or empty.
	Photo string
	// Meta is the rating and difficulty line.
	Meta string
	// Lang is the site's language code, set when the search mixes
	// languages.
	Lang string
	// State is one of the Discover hit states; Href is the recipe
	// (DiscoverLibrary) or the import (the others).
	State string
	Href  string
}

// DiscoverPreviewView previews a recipe on a site as importing it would
// start it, before anything is saved.
type DiscoverPreviewView struct {
	// Site is the recipe's site.
	Site string
	// Search is the search the preview was opened from, for the way back
	// and the import form.
	Search WebSearch
	// Hit is the recipe page with the viewer's import state.
	Hit DiscoverHit
	// SourceHost is the recipe page's host, for the link to it.
	SourceHost string
	// Recipe is the recipe as importing would start it; nil when the page
	// could not be read (NoRecipe tells a page without recipe data apart
	// from one that could not be fetched).
	Recipe   *ReadOnlyRecipeView
	NoRecipe bool
	// AI is whether AI import is on, which can still import a page
	// without recipe data.
	AI bool
	// Alert is a failed import's message.
	Alert string
}

// DiscoverPreviewHref is the preview of the recipe at link, opened from
// the search w.
func DiscoverPreviewHref(link string, w WebSearch) string {
	v := w.values()
	v.Set("u", link)
	return DiscoverPath + "/preview?" + v.Encode()
}

// DiscoverPhotoHref is a result photo served through mise, as the
// content security policy allows only the app's own images.
func DiscoverPhotoHref(photo string) string {
	return DiscoverPath + "/photo?" + url.Values{"u": {photo}}.Encode()
}

// searchWebLive opens the "From the web" section's live stream for the
// $wq, $site and $wpages signals. It reads all three, so Datastar reopens
// the stream (cancelling the open one) when the query settles, a chip is
// picked or "More results" is pressed.
const searchWebLive = "$wq; $site; $wpages; @get('/live?page=search-web', {filterSignals: {include: /^(wq|site|wpages)$/}, retryMaxCount: 1000})"

// webPick is a site chip's click with JavaScript: the stream reopens for
// the site's first page.
func webPick(site string) string { return "$site = " + jsString(site) + "; $wpages = 1" }

// webMore is "More results" with JavaScript: the stream reopens with one
// page more.
func webMore(w WebSearch) string { return "$wpages = " + itoa(w.Pages+1) }

// discoverPreviewLive is the preview's live stream, which re-renders its
// import state as the viewer's import progresses.
func discoverPreviewLive(v DiscoverPreviewView) string {
	q := v.Search.values()
	q.Set("page", "discover-preview")
	q.Set("u", v.Hit.URL)
	return liveAction("/live?" + q.Encode())
}
