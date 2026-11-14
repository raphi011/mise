package discover

import (
	"bytes"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"

	"github.com/raphi011/mise/internal/ai"
)

// find returns the first element under n (n included) that match accepts,
// in document order.
func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, match); f != nil {
			return f
		}
	}
	return nil
}

// findAll returns the elements under n that match accepts, not looking
// inside a match.
func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	if n.Type == html.ElementNode && match(n) {
		return []*html.Node{n}
	}
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, findAll(c, match)...)
	}
	return out
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == name {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for c := range strings.FieldsSeq(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// text is n's text content with white space collapsed.
func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// parseHTML parses a fetched page in the charset it declares.
func parseHTML(f ai.Fetched) (*html.Node, error) {
	r, err := charset.NewReader(bytes.NewReader(f.Body), f.ContentType)
	if err != nil {
		r = bytes.NewReader(f.Body)
	}
	return html.Parse(r)
}

// attrURL resolves n's attribute name against base; ok is false when the
// attribute is empty or not a URL.
func attrURL(n *html.Node, name string, base *url.URL) (*url.URL, bool) {
	v := strings.TrimSpace(attr(n, name))
	if v == "" || strings.HasPrefix(v, "data:") {
		return nil, false
	}
	u, err := base.Parse(v)
	if err != nil {
		return nil, false
	}
	return u, true
}

// firstSrcset is the first URL of a srcset attribute value.
func firstSrcset(v string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(v), ",")
	u, _, _ := strings.Cut(strings.TrimSpace(first), " ")
	return u
}

// plainText is the text of an HTML fragment, white space collapsed.
func plainText(fragment string) string {
	nodes, err := html.ParseFragment(strings.NewReader(fragment), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return ""
	}
	var parts []string
	for _, n := range nodes {
		if t := text(n); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// clip cuts s to at most n characters at a word boundary, marking the cut
// with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:–-") + "…"
}
