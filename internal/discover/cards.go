package discover

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
)

// cardSite is a site whose search is a server-rendered page of result
// cards. Rather than reading one site's markup, it finds the links to the
// site's recipe pages and, for each recipe, the largest element around its
// links that holds no other recipe: the card. A card gives the hit's
// title, photo and snippet. When the page lists its results in a JSON-LD
// ItemList, that list decides which recipes are hits and in what order,
// so links in menus and teasers are left out.
type cardSite struct {
	name string
	lang language.Tag
	// search is the result page for q, page counting from 1.
	search func(q string, page int) string
	// recipeHost and recipePath match the site's recipe pages.
	recipeHost string
	recipePath *regexp.Regexp
	photoHosts []string
	// itemList: hits are the page's JSON-LD ItemList.
	itemList bool
	// next reports whether a link on result page page goes to the page
	// after it. Without it only rel="next" tells there is one.
	next func(u *url.URL, page int) bool
	// snippet matches a card's description, when cards have one.
	snippet func(*html.Node) bool
	// photoSize, when set, asks the site's image service for a photo at
	// the size results show.
	photoSize func(*url.URL)
}

// maxCardDepth bounds how far above a recipe link its card may start, so a
// page with a single result does not make the whole page its card.
const maxCardDepth = 8

// maxSnippet is the longest snippet kept, in characters.
const maxSnippet = 200

func (s cardSite) Name() string { return s.name }

func (s cardSite) Language() language.Tag { return s.lang }

func (s cardSite) SearchURL(q string, page int) (string, ai.Kind) {
	return s.search(q, page), ai.KindPage
}

func (s cardSite) PhotoHost(host string) bool {
	return slices.ContainsFunc(s.photoHosts, func(h string) bool { return strings.EqualFold(h, host) })
}

func (s cardSite) RecipeURL(u *url.URL) (string, bool) {
	if !strings.EqualFold(u.Host, s.recipeHost) || !s.recipePath.MatchString(u.Path) {
		return "", false
	}
	c := url.URL{Scheme: "https", Host: s.recipeHost, Path: u.Path}
	return c.String(), true
}

func (s cardSite) ParseResults(f ai.Fetched, page int) (ResultPage, error) {
	doc, err := parseHTML(f)
	if err != nil {
		return ResultPage{}, err
	}
	base := f.URL
	if base == nil {
		base = &url.URL{Scheme: "https", Host: s.recipeHost}
	}
	// The recipe each link goes to, and per element the one recipe linked
	// inside it ("" for none, multiple for more than one).
	links := map[*html.Node]string{}
	var order []string
	byRecipe := map[string][]*html.Node{}
	for _, a := range findAll(doc, func(n *html.Node) bool { return n.DataAtom == atom.A }) {
		u, ok := attrURL(a, "href", base)
		if !ok {
			continue
		}
		recipe, ok := s.RecipeURL(u)
		if !ok {
			continue
		}
		links[a] = recipe
		if byRecipe[recipe] == nil {
			order = append(order, recipe)
		}
		byRecipe[recipe] = append(byRecipe[recipe], a)
	}
	inside := map[*html.Node]string{}
	recipesInside(doc, links, inside)

	var listed map[string]listItem
	if s.itemList {
		listed = map[string]listItem{}
		order = order[:0]
		for _, it := range itemList(doc, base) {
			u, err := url.Parse(it.URL)
			if err != nil {
				continue
			}
			recipe, ok := s.RecipeURL(u)
			if _, dup := listed[recipe]; !ok || dup {
				continue
			}
			listed[recipe] = it
			order = append(order, recipe)
		}
	}

	var res ResultPage
	for _, recipe := range order {
		h := Hit{URL: recipe}
		var card *html.Node
		for _, a := range byRecipe[recipe] {
			c := cardOf(a, recipe, inside)
			if card == nil || (s.photo(card, base) == "" && s.photo(c, base) != "") {
				card = c
			}
		}
		it := listed[recipe]
		h.Title = firstOf(cardTitle(card), it.Name, linkTitle(byRecipe[recipe]))
		// The card's photo is sized for a list; the ItemList's is often the
		// original.
		h.Photo = s.photo(card, base)
		if p, err := url.Parse(it.Image); h.Photo == "" && err == nil && it.Image != "" && s.PhotoHost(p.Host) {
			h.Photo = it.Image
		}
		if p, err := url.Parse(h.Photo); s.photoSize != nil && h.Photo != "" && err == nil {
			s.photoSize(p)
			h.Photo = p.String()
		}
		h.Snippet = clip(firstOf(it.Description, s.description(card)), maxSnippet)
		if h.Title == "" {
			continue
		}
		res.Hits = append(res.Hits, h)
	}
	res.More = s.more(doc, base, page)
	return res, nil
}

// multiple marks an element holding links to more than one recipe.
const multiple = "\x00"

// recipesInside fills inside with the one recipe linked under each
// element, or multiple, and returns n's.
func recipesInside(n *html.Node, links map[*html.Node]string, inside map[*html.Node]string) string {
	r := links[n]
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch cr := recipesInside(c, links, inside); {
		case cr == "":
		case r == "":
			r = cr
		case r != cr:
			r = multiple
		}
	}
	if r != "" {
		inside[n] = r
	}
	return r
}

// cardOf is the largest element around link a that links to recipe alone.
func cardOf(a *html.Node, recipe string, inside map[*html.Node]string) *html.Node {
	card := a
	for d := 0; d < maxCardDepth; d++ {
		p := card.Parent
		if p == nil || p.Type != html.ElementNode || p.DataAtom == atom.Body || inside[p] != recipe {
			break
		}
		card = p
	}
	return card
}

// cardTitle is the text of a card's first heading.
func cardTitle(card *html.Node) string {
	if card == nil {
		return ""
	}
	if h := find(card, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5:
			return true
		}
		return false
	}); h != nil {
		return text(h)
	}
	return ""
}

// linkTitle names a recipe from its links: the shortest link text that
// is not the whole card, else an image's alt text or a link's title.
func linkTitle(links []*html.Node) string {
	var best string
	for _, a := range links {
		if t := text(a); t != "" && len([]rune(t)) <= 120 && (best == "" || len(t) < len(best)) {
			best = t
		}
	}
	if best != "" {
		return best
	}
	for _, a := range links {
		if img := find(a, func(n *html.Node) bool { return n.DataAtom == atom.Img }); img != nil {
			if alt := strings.TrimSpace(attr(img, "alt")); alt != "" {
				return alt
			}
		}
	}
	for _, a := range links {
		if t := strings.TrimSpace(attr(a, "title")); t != "" {
			return t
		}
	}
	return ""
}

// photo is the first image in card that is on one of the site's photo
// hosts, from its lazy-loading attributes, src or srcset.
func (s cardSite) photo(card *html.Node, base *url.URL) string {
	var out string
	find(card, func(n *html.Node) bool {
		if n.DataAtom != atom.Img && n.DataAtom != atom.Source {
			return false
		}
		for _, name := range []string{"data-src", "data-lazy-src", "src", "data-srcset", "srcset"} {
			v := attr(n, name)
			if strings.HasSuffix(name, "srcset") {
				v = firstSrcset(v)
			}
			v = strings.TrimSpace(v)
			if v == "" || strings.HasPrefix(v, "data:") {
				continue
			}
			if u, err := base.Parse(v); err == nil && s.PhotoHost(u.Host) {
				out = u.String()
				return true
			}
		}
		return false
	})
	return out
}

// description is the text of a card's description.
func (s cardSite) description(card *html.Node) string {
	if card == nil || s.snippet == nil {
		return ""
	}
	if n := find(card, s.snippet); n != nil {
		return text(n)
	}
	return ""
}

// byClass matches elements of a class.
func byClass(class string) func(*html.Node) bool {
	return func(n *html.Node) bool { return hasClass(n, class) }
}

// byTag matches elements of a tag.
func byTag(a atom.Atom) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.DataAtom == a }
}

// more reports whether the page has a next page: a rel="next" link or a
// link next accepts.
func (s cardSite) more(doc *html.Node, base *url.URL, page int) bool {
	return find(doc, func(n *html.Node) bool {
		if n.DataAtom != atom.A && n.DataAtom != atom.Link {
			return false
		}
		if slices.Contains(strings.Fields(attr(n, "rel")), "next") {
			return true
		}
		if s.next == nil || n.DataAtom != atom.A {
			return false
		}
		u, ok := attrURL(n, "href", base)
		return ok && s.next(u, page)
	}) != nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
