package discover

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/ai"
)

// wordPress is a recipe blog on WordPress. Its posts are searched through
// the WordPress REST API (/wp-json/wp/v2/posts?search=…), which answers
// the same way on every such blog; each post's featured image comes
// embedded. Recipe pages carry the recipe plugin's JSON-LD.
type wordPress struct {
	name string
	lang language.Tag
	base *url.URL
	// photoHosts are where the featured images live; the blog's own host
	// when empty.
	photoHosts []string
}

// wordPressPerPage is how many posts a result page asks for.
const wordPressPerPage = 20

// wordPressPost matches a post's address: one or more path segments with
// a trailing slash. Archive and system paths are excluded below.
var wordPressPost = regexp.MustCompile(`^/(?:[a-z0-9%_-]+/)+$`)

// wordPressOther are first path segments that are not posts.
var wordPressOther = []string{"wp-admin", "wp-content", "wp-includes", "wp-json", "category", "tag", "author", "page", "feed", "search", "comments"}

func (s wordPress) Name() string { return s.name }

func (s wordPress) Language() language.Tag { return s.lang }

func (s wordPress) SearchURL(q string, page int) (string, ai.Kind) {
	v := url.Values{
		"search":   {q},
		"per_page": {strconv.Itoa(wordPressPerPage)},
		"_embed":   {"wp:featuredmedia"},
		"_fields":  {"link,title,excerpt,_links,_embedded"},
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return s.base.JoinPath("wp-json", "wp", "v2", "posts").String() + "?" + v.Encode(), ai.KindJSON
}

func (s wordPress) PhotoHost(host string) bool {
	if len(s.photoHosts) == 0 {
		return strings.EqualFold(host, s.base.Host)
	}
	for _, h := range s.photoHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

func (s wordPress) RecipeURL(u *url.URL) (string, bool) {
	if !strings.EqualFold(u.Host, s.base.Host) || !wordPressPost.MatchString(u.Path) {
		return "", false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	for _, o := range wordPressOther {
		if first == o {
			return "", false
		}
	}
	c := url.URL{Scheme: s.base.Scheme, Host: s.base.Host, Path: u.Path}
	return c.String(), true
}

type wordPressResult struct {
	Link     string                    `json:"link"`
	Title    struct{ Rendered string } `json:"title"`
	Excerpt  struct{ Rendered string } `json:"excerpt"`
	Embedded struct {
		Media []struct {
			SourceURL    string `json:"source_url"`
			MediaDetails struct {
				Sizes map[string]struct {
					SourceURL string `json:"source_url"`
				} `json:"sizes"`
			} `json:"media_details"`
		} `json:"wp:featuredmedia"`
	} `json:"_embedded"`
}

// wordPressSizes are the featured image sizes tried for a result photo,
// smallest fitting first.
var wordPressSizes = []string{"medium_large", "medium", "large"}

func (s wordPress) ParseResults(f ai.Fetched, page int) (ResultPage, error) {
	var posts []wordPressResult
	if err := json.Unmarshal(f.Body, &posts); err != nil {
		return ResultPage{}, fmt.Errorf("wordpress results: %w", err)
	}
	var res ResultPage
	for _, p := range posts {
		u, err := url.Parse(p.Link)
		if err != nil {
			continue
		}
		recipe, ok := s.RecipeURL(u)
		if !ok {
			continue
		}
		h := Hit{URL: recipe, Title: plainText(p.Title.Rendered), Snippet: clip(plainText(p.Excerpt.Rendered), maxSnippet)}
		if h.Title == "" {
			continue
		}
		if len(p.Embedded.Media) > 0 {
			m := p.Embedded.Media[0]
			photo := m.SourceURL
			for _, size := range wordPressSizes {
				if sz, ok := m.MediaDetails.Sizes[size]; ok && sz.SourceURL != "" {
					photo = sz.SourceURL
					break
				}
			}
			if pu, err := url.Parse(photo); err == nil && photo != "" && s.PhotoHost(pu.Host) {
				h.Photo = pu.String()
			}
		}
		res.Hits = append(res.Hits, h)
	}
	if total, err := strconv.Atoi(f.Header.Get("X-WP-TotalPages")); err == nil {
		res.More = page < total
	} else {
		res.More = len(posts) == wordPressPerPage
	}
	return res, nil
}
