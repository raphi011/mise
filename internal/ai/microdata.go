package ai

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// microdataRecipe returns the first schema.org Recipe item in a page's
// microdata (itemscope/itemprop attributes), as the node map JSON-LD
// decodes to, so recipeFromNode reads both the same way. Every property
// maps to an array of values: a nested item becomes a map with its @type,
// a URL property an absolute URL, and any other element its content as
// markup, which the JSON-LD helpers already clean up.
func microdataRecipe(doc *html.Node, base *url.URL) map[string]any {
	root := findItem(doc, isRecipeType)
	if root == nil {
		return nil
	}
	md := microdata{base: base, ids: map[string]*html.Node{}}
	md.indexIDs(doc)
	return md.item(root, 0)
}

type microdata struct {
	base *url.URL
	// ids maps element ids to elements, for itemref.
	ids map[string]*html.Node
}

func (md microdata) indexIDs(n *html.Node) {
	if n.Type == html.ElementNode {
		if id := attr(n, "id"); id != "" {
			if _, dup := md.ids[id]; !dup {
				md.ids[id] = n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		md.indexIDs(c)
	}
}

// findItem returns the first element with itemscope whose itemtype
// matches, in document order.
func findItem(n *html.Node, match func(string) bool) *html.Node {
	if n.Type == html.ElementNode && hasAttr(n, "itemscope") {
		for t := range strings.FieldsSeq(attr(n, "itemtype")) {
			if match(t) {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findItem(c, match); f != nil {
			return f
		}
	}
	return nil
}

// item reads the properties of the item rooted at n.
func (md microdata) item(n *html.Node, depth int) map[string]any {
	node := map[string]any{}
	if types := strings.Fields(attr(n, "itemtype")); len(types) > 0 {
		node["@type"] = types[0]
	}
	if depth > maxDepth {
		return node
	}
	for _, p := range md.properties(n) {
		var v any
		if hasAttr(p, "itemscope") {
			v = md.item(p, depth+1)
		} else {
			v = md.value(p)
		}
		for name := range strings.FieldsSeq(attr(p, "itemprop")) {
			// Absolute property names (https://schema.org/name) name the
			// same property as their last segment.
			name = name[strings.LastIndexByte(name, '/')+1:]
			vals, _ := node[name].([]any)
			node[name] = append(vals, v)
		}
	}
	return node
}

// properties returns the elements carrying the item's properties: those
// with itemprop among its descendants and the elements its itemref names,
// not descending into nested items, which own their own properties.
func (md microdata) properties(root *html.Node) []*html.Node {
	var out []*html.Node
	seen := map[*html.Node]bool{root: true}
	var crawl func(n *html.Node)
	crawl = func(n *html.Node) {
		if seen[n] {
			return
		}
		seen[n] = true
		if attr(n, "itemprop") != "" {
			out = append(out, n)
		}
		if !hasAttr(n, "itemscope") {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					crawl(c)
				}
			}
		}
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			crawl(c)
		}
	}
	for id := range strings.FieldsSeq(attr(root, "itemref")) {
		if ref := md.ids[id]; ref != nil {
			crawl(ref)
		}
	}
	return out
}

// value is a property element's value as the microdata spec defines it,
// except that text content keeps its markup.
func (md microdata) value(n *html.Node) string {
	switch n.DataAtom {
	case atom.Meta:
		return attr(n, "content")
	case atom.Audio, atom.Embed, atom.Iframe, atom.Img, atom.Source, atom.Track, atom.Video:
		return resolve(md.base, attr(n, "src"))
	case atom.A, atom.Area, atom.Link:
		return resolve(md.base, attr(n, "href"))
	case atom.Object:
		return resolve(md.base, attr(n, "data"))
	case atom.Data, atom.Meter:
		return attr(n, "value")
	case atom.Time:
		if hasAttr(n, "datetime") {
			return attr(n, "datetime")
		}
	}
	var b strings.Builder
	writeMarkup(&b, n)
	return b.String()
}

// writeMarkup writes n's content as markup without attributes: tags are
// kept so cleanText and stripTagsToLines treat it like a JSON-LD string
// holding HTML, and white space is collapsed because line breaks in the
// page source carry no meaning.
func writeMarkup(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			b.WriteString(html.EscapeString(collapseSpace(c.Data)))
		case html.ElementNode:
			switch c.DataAtom {
			case atom.Script, atom.Style, atom.Template, atom.Noscript:
				continue
			}
			b.WriteString("<" + c.Data + ">")
			if !isVoid(c.DataAtom) {
				writeMarkup(b, c)
				b.WriteString("</" + c.Data + ">")
			}
		}
	}
}

// collapseSpace turns every run of white space into one space, keeping a
// leading or trailing one so words in adjacent nodes stay apart.
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

func isVoid(a atom.Atom) bool {
	switch a {
	case atom.Area, atom.Base, atom.Br, atom.Col, atom.Embed, atom.Hr, atom.Img, atom.Input,
		atom.Link, atom.Meta, atom.Source, atom.Track, atom.Wbr:
		return true
	}
	return false
}

func hasAttr(n *html.Node, name string) bool {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, name) {
			return true
		}
	}
	return false
}
