package ai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/language"
)

// MaxTextBytes caps page text and pasted text handed to the model.
const MaxTextBytes = 50 << 10

// Page is what an import uses from a fetched page.
type Page struct {
	URL *url.URL
	// Title is the document title.
	Title string
	// Lang is the document's language (<html lang>), if it names one.
	Lang language.Tag
	// Recipe is the first schema.org Recipe in the page's JSON-LD, or nil.
	Recipe *Recipe
	// Image is og:image, a fallback photo.
	Image string
	// Text is the readable text (scripts, styles, navigation stripped),
	// capped at MaxTextBytes, for the model.
	Text string
}

// ParsePage reads a fetched page. Plain-text responses become Text only.
func ParsePage(res Fetched) Page {
	p := Page{URL: res.URL}
	r, err := charset.NewReader(bytes.NewReader(res.Body), res.ContentType)
	if err != nil {
		r = bytes.NewReader(res.Body)
	}
	if strings.HasPrefix(strings.ToLower(res.ContentType), "text/plain") {
		b, _ := io.ReadAll(r)
		p.Text = capText(string(b))
		return p
	}
	doc, err := html.Parse(r)
	if err != nil {
		return p
	}
	var text strings.Builder
	var walk func(n *html.Node, skip bool)
	walk = func(n *html.Node, skip bool) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script:
				if p.Recipe == nil && strings.EqualFold(attr(n, "type"), "application/ld+json") && n.FirstChild != nil {
					p.Recipe = recipeFromJSONLD([]byte(n.FirstChild.Data), res.URL)
				}
				return
			case atom.Style, atom.Noscript, atom.Svg, atom.Template, atom.Iframe:
				return
			case atom.Nav, atom.Header, atom.Footer, atom.Aside, atom.Form:
				skip = true
			case atom.Html:
				if tag, err := language.Parse(attr(n, "lang")); err == nil {
					p.Lang = tag
				}
			case atom.Title:
				if p.Title == "" && n.FirstChild != nil {
					p.Title = cleanText(n.FirstChild.Data)
				}
				return
			case atom.Meta:
				if p.Image == "" && (attr(n, "property") == "og:image" || attr(n, "name") == "og:image") {
					p.Image = resolve(res.URL, attr(n, "content"))
				}
			}
			if isBlock(n.DataAtom) && !skip {
				text.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode && !skip {
			text.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, skip)
		}
	}
	walk(doc, false)
	p.Text = capText(collapseLines(text.String()))
	return p
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Br, atom.Li, atom.Tr, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Section, atom.Article, atom.Ul, atom.Ol, atom.Table, atom.Blockquote, atom.Pre, atom.Dd, atom.Dt:
		return true
	}
	return false
}

// collapseLines trims every line, collapses runs of spaces and drops
// empty lines.
func collapseLines(s string) string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// capText cuts s to MaxTextBytes at a rune boundary.
func capText(s string) string {
	if len(s) <= MaxTextBytes {
		return s
	}
	s = s[:MaxTextBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// resolve makes ref absolute against base; it returns "" for anything but
// http(s).
func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// cleanText unescapes HTML entities, strips tags and collapses white space,
// for JSON-LD strings that carry markup.
func cleanText(s string) string {
	if strings.ContainsAny(s, "<&") {
		if doc, err := html.Parse(strings.NewReader("<body>" + s)); err == nil {
			var b strings.Builder
			var walk func(n *html.Node)
			walk = func(n *html.Node) {
				if n.Type == html.TextNode {
					b.WriteString(n.Data)
				}
				if n.Type == html.ElementNode && isBlock(n.DataAtom) {
					b.WriteByte(' ')
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(doc)
			s = b.String()
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

// decodeJSON is json.Unmarshal that tolerates the raw control characters
// some sites leave in JSON-LD strings.
func decodeJSON(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err == nil {
		return nil
	}
	clean := bytes.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, data)
	return json.Unmarshal(clean, v)
}
