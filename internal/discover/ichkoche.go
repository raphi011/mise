package discover

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
)

// ichkoche is ichkoche.at, an Austrian recipe site. Its search
// (/suche/rezepte?q=…&page=…) is a server-rendered page of 20
// <article class="box"> cards; its recipe pages carry microdata.
type ichkoche struct {
	base      *url.URL
	photoHost string
}

// Ichkoche is ichkoche.at.
func Ichkoche() Site {
	return NewIchkoche(&url.URL{Scheme: "https", Host: "www.ichkoche.at"}, "assets.ichkoche.at")
}

// NewIchkoche is ichkoche.at served from base with photos from photoHost
// (a host with its port, if any), for tests.
func NewIchkoche(base *url.URL, photoHost string) Site {
	return ichkoche{base: base, photoHost: photoHost}
}

func (ichkoche) Name() string { return "ichkoche.at" }

func (ichkoche) Language() language.Tag { return language.German }

func (s ichkoche) SearchURL(q string, page int) (string, ai.Kind) {
	v := url.Values{"q": {q}}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return s.base.JoinPath("suche", "rezepte").String() + "?" + v.Encode(), ai.KindPage
}

func (s ichkoche) PhotoHost(host string) bool { return strings.EqualFold(host, s.photoHost) }

func (s ichkoche) RecipeURL(u *url.URL) (string, bool) {
	if !strings.EqualFold(u.Host, s.base.Host) || !ichkocheRecipe.MatchString(u.Path) {
		return "", false
	}
	// The same form for every spelling of the address, so a hit matches
	// its imports and the recipe saved from it.
	c := url.URL{Scheme: s.base.Scheme, Host: s.base.Host, Path: u.Path}
	return c.String(), true
}

// ichkocheRecipe matches recipe paths. Results also list members' own
// cookbook entries (/kochbuch/…), which robots.txt asks crawlers to leave
// alone.
var ichkocheRecipe = regexp.MustCompile(`^/[a-z0-9-]+-rezept-\d+$`)

var (
	starsClass   = regexp.MustCompile(`^stars_([0-5])$`)
	firstInteger = regexp.MustCompile(`\d+`)
)

func (s ichkoche) ParseResults(f ai.Fetched, _ int) (ResultPage, error) {
	doc, err := parseHTML(f)
	if err != nil {
		return ResultPage{}, err
	}
	base := f.URL
	if base == nil {
		base = s.base
	}
	var res ResultPage
	seen := map[string]bool{}
	for _, card := range findAll(doc, func(n *html.Node) bool { return n.DataAtom == atom.Article && hasClass(n, "box") }) {
		link := find(card, func(n *html.Node) bool { return n.DataAtom == atom.A && attr(n, "href") != "" })
		if link == nil {
			continue
		}
		u, err := base.Parse(attr(link, "href"))
		if err != nil {
			continue
		}
		recipe, ok := s.RecipeURL(u)
		if !ok || seen[recipe] {
			continue
		}
		seen[recipe] = true
		h := Hit{URL: recipe}
		if t := find(card, func(n *html.Node) bool { return n.DataAtom == atom.H3 }); t != nil {
			h.Title = text(t)
		}
		if h.Title == "" {
			h.Title = strings.TrimSpace(attr(link, "title"))
		}
		if h.Title == "" {
			continue
		}
		if img := find(card, func(n *html.Node) bool { return n.DataAtom == atom.Img }); img != nil {
			src := strings.TrimSpace(attr(img, "data-src"))
			if src == "" {
				src = strings.TrimSpace(attr(img, "src"))
			}
			if p, err := base.Parse(src); err == nil && src != "" && s.PhotoHost(p.Host) {
				h.Photo = p.String()
			}
		}
		if d := find(card, func(n *html.Node) bool { return n.DataAtom == atom.P && hasClass(n, "description") }); d != nil {
			h.Snippet = text(d)
		}
		if r := find(card, func(n *html.Node) bool { return hasClass(n, "rating") }); r != nil {
			if st := find(r, func(n *html.Node) bool { return hasClass(n, "stars") }); st != nil {
				for c := range strings.FieldsSeq(attr(st, "class")) {
					if m := starsClass.FindStringSubmatch(c); m != nil {
						h.Stars, _ = strconv.Atoi(m[1])
					}
				}
			}
			if m := firstInteger.FindString(strings.ReplaceAll(text(r), ".", "")); m != "" {
				h.Ratings, _ = strconv.Atoi(m)
			}
		}
		if l := find(card, func(n *html.Node) bool { return hasClass(n, "spoons") }); l != nil {
			h.Level = text(l)
		}
		res.Hits = append(res.Hits, h)
	}
	pager := find(doc, func(n *html.Node) bool { return hasClass(n, "pagination") })
	res.More = pager != nil && find(pager, func(n *html.Node) bool { return n.DataAtom == atom.Li && hasClass(n, "next") }) != nil
	return res, nil
}
