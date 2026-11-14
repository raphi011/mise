package discover

import (
	"encoding/json"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// listItem is an entry of a schema.org ItemList in a page's JSON-LD.
type listItem struct {
	URL         string
	Name        string
	Description string
	Image       string
}

// itemList returns the entries of the first schema.org ItemList in the
// page's JSON-LD that has any, with URLs resolved against base. Many
// sites describe their search results this way for search engines.
func itemList(doc *html.Node, base *url.URL) []listItem {
	scripts := findAll(doc, func(n *html.Node) bool {
		return n.DataAtom == atom.Script && strings.EqualFold(strings.TrimSpace(attr(n, "type")), "application/ld+json")
	})
	for _, sc := range scripts {
		if sc.FirstChild == nil {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(sc.FirstChild.Data), &v); err != nil {
			continue
		}
		if items := findItemList(v, base); len(items) > 0 {
			return items
		}
	}
	return nil
}

func findItemList(v any, base *url.URL) []listItem {
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			if items := findItemList(e, base); len(items) > 0 {
				return items
			}
		}
	case map[string]any:
		if hasType(v, "ItemList") {
			var out []listItem
			for _, e := range asList(v["itemListElement"]) {
				if it, ok := readListItem(e, base); ok {
					out = append(out, it)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		if g, ok := v["@graph"]; ok {
			return findItemList(g, base)
		}
	}
	return nil
}

// readListItem reads a ListItem, whose fields may sit on the entry itself
// or on its "item".
func readListItem(v any, base *url.URL) (listItem, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return listItem{}, false
	}
	inner, _ := m["item"].(map[string]any)
	get := func(keys ...string) any {
		for _, src := range []map[string]any{m, inner} {
			for _, k := range keys {
				if e, ok := src[k]; ok && e != nil && e != "" {
					return e
				}
			}
		}
		return nil
	}
	raw := str(get("url", "@id"))
	u, err := base.Parse(raw)
	if raw == "" || err != nil {
		return listItem{}, false
	}
	it := listItem{URL: u.String(), Name: html.UnescapeString(str(get("name"))), Description: html.UnescapeString(str(get("description")))}
	if img := image(get("image")); img != "" {
		if iu, err := base.Parse(img); err == nil {
			it.Image = iu.String()
		}
	}
	return it, true
}

func hasType(m map[string]any, t string) bool {
	for _, e := range asList(m["@type"]) {
		if str(e) == t {
			return true
		}
	}
	return false
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	if v == nil {
		return nil
	}
	return []any{v}
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// image is the first URL of a schema.org image value: a URL, an
// ImageObject or a list of either.
func image(v any) string {
	for _, e := range asList(v) {
		switch e := e.(type) {
		case string:
			if s := strings.TrimSpace(e); s != "" {
				return s
			}
		case map[string]any:
			if s := str(e["url"]); s != "" {
				return s
			}
			if s := str(e["contentUrl"]); s != "" {
				return s
			}
		}
	}
	return ""
}
