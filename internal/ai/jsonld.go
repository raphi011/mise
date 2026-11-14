package ai

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Recipe is a recipe as read from a source (JSON-LD, microdata or the
// model), before
// its ingredients are matched to items.
type Recipe struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	PrepMin     int      `json:"prep_min,omitempty"` // 0: unknown
	CookMin     int      `json:"cook_min,omitempty"`
	Servings    int      `json:"servings,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	ImageURL    string   `json:"image_url,omitempty"`
	Ingredients []string `json:"ingredients,omitempty"`
	Steps       []Step   `json:"steps,omitempty"`
	// Keywords are tag candidates (keywords, category, cuisine).
	Keywords []string `json:"keywords,omitempty"`
}

// Step is one step of a recipe source.
type Step struct {
	Text         string `json:"text"`
	TimerSeconds int    `json:"timer_seconds,omitempty"`
}

// recipeFromJSONLD returns the first schema.org Recipe in a JSON-LD
// document, or nil.
func recipeFromJSONLD(data []byte, base *url.URL) *Recipe {
	var doc any
	if err := decodeJSON(data, &doc); err != nil {
		return nil
	}
	node := findRecipe(doc, 0)
	if node == nil {
		return nil
	}
	return recipeFromNode(node, base)
}

// recipeFromNode reads a schema.org Recipe node, decoded from JSON-LD or
// built from microdata. It returns nil without a name or ingredients.
func recipeFromNode(node map[string]any, base *url.URL) *Recipe {
	r := &Recipe{
		Name:        cleanText(str(node["name"])),
		Description: cleanText(str(node["description"])),
		SourceURL:   resolve(base, str(node["url"])),
		ImageURL:    resolve(base, imageURL(node["image"])),
	}
	prep, cook, total := duration(node["prepTime"]), duration(node["cookTime"]), duration(node["totalTime"])
	if cook == 0 && total > prep {
		cook = total - prep
	}
	r.PrepMin, r.CookMin = prep, cook
	r.Servings = servings(node["recipeYield"])
	ings := node["recipeIngredient"]
	if ings == nil {
		ings = node["ingredients"]
	}
	for _, s := range strs(ings) {
		if s = cleanText(s); s != "" {
			r.Ingredients = append(r.Ingredients, s)
		}
	}
	r.Steps = instructions(node["recipeInstructions"], 0)
	for _, field := range []string{"recipeCategory", "recipeCuisine", "keywords"} {
		for _, v := range strs(node[field]) {
			for k := range strings.SplitSeq(v, ",") {
				if k = cleanText(k); k != "" {
					r.Keywords = append(r.Keywords, k)
				}
			}
		}
	}
	if r.Name == "" || len(r.Ingredients) == 0 {
		return nil
	}
	return r
}

// maxDepth bounds recursion into hostile JSON.
const maxDepth = 8

// findRecipe searches objects, arrays and @graph for @type Recipe.
func findRecipe(v any, depth int) map[string]any {
	if depth > maxDepth {
		return nil
	}
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			if r := findRecipe(e, depth+1); r != nil {
				return r
			}
		}
	case map[string]any:
		if slices.ContainsFunc(strs(v["@type"]), isRecipeType) {
			return v
		}
		for _, key := range []string{"@graph", "mainEntity", "mainEntityOfPage"} {
			if r := findRecipe(v[key], depth+1); r != nil {
				return r
			}
		}
	}
	return nil
}

// isRecipeType reports whether a type names schema.org Recipe, short or
// as a URL.
func isRecipeType(t string) bool {
	return t == "Recipe" || strings.HasSuffix(t, "/Recipe")
}

// str returns a string, a number as text, or the first string of an array.
func str(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		for _, e := range v {
			if s := str(e); s != "" {
				return s
			}
		}
	case map[string]any:
		if s, ok := v["@value"].(string); ok {
			return s
		}
	}
	return ""
}

// strs returns every string of a string or array.
func strs(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case float64:
		return []string{str(v)}
	case []any:
		var out []string
		for _, e := range v {
			if s := str(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// imageURL reads schema.org image: a URL, an ImageObject, or an array of
// either.
func imageURL(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []any:
		for _, e := range v {
			if s := imageURL(e); s != "" {
				return s
			}
		}
	case map[string]any:
		if s := str(v["url"]); s != "" {
			return s
		}
		return str(v["contentUrl"])
	}
	return ""
}

var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// duration reads an ISO 8601 duration such as PT1H30M, in whole minutes.
func duration(v any) int {
	m := isoDuration.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(str(v))))
	if m == nil {
		return 0
	}
	f := func(s string) float64 { x, _ := strconv.ParseFloat(s, 64); return x }
	mins := f(m[1])*24*60 + f(m[2])*60 + f(m[3]) + f(m[4])/60
	if mins < 0 || mins > 24*60 {
		return 0
	}
	return int(mins + 0.5)
}

var firstNumber = regexp.MustCompile(`\d+`)

// servings reads recipeYield ("4", 4, "4 servings", ["4", "4 servings"]).
func servings(v any) int {
	for _, s := range strs(v) {
		if m := firstNumber.FindString(s); m != "" {
			n, _ := strconv.Atoi(m)
			if n >= 1 && n <= 100 {
				return n
			}
		}
	}
	return 0
}

// instructions reads recipeInstructions: text (one step per line), an
// array of strings, HowToStep objects or HowToSections of them.
func instructions(v any, depth int) []Step {
	if depth > maxDepth {
		return nil
	}
	var out []Step
	switch v := v.(type) {
	case string:
		for line := range strings.SplitSeq(stripTagsToLines(v), "\n") {
			if line = stepText(line); line != "" {
				out = append(out, Step{Text: line})
			}
		}
	case []any:
		for _, e := range v {
			out = append(out, instructions(e, depth+1)...)
		}
	case map[string]any:
		if items, ok := v["itemListElement"]; ok {
			return instructions(items, depth+1)
		}
		text := stepText(str(v["text"]))
		if text == "" {
			text = stepText(str(v["name"]))
		}
		if text != "" {
			out = append(out, Step{Text: text})
		}
	}
	return out
}

// stepText cleans a step's text. A step that is nothing but a link points
// elsewhere ("Watch the video") rather than saying what to do, so it
// comes back empty.
func stepText(s string) string {
	if linkOnly(s) {
		return ""
	}
	return cleanText(s)
}

// linkOnly reports whether all the text in a piece of markup sits inside
// links.
func linkOnly(s string) bool {
	if !strings.Contains(s, "<") {
		return false
	}
	doc, err := html.Parse(strings.NewReader("<body>" + s))
	if err != nil {
		return false
	}
	linked := false
	var walk func(n *html.Node, inLink bool) bool
	walk = func(n *html.Node, inLink bool) bool {
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
			if !inLink {
				return false
			}
			linked = true
		}
		inLink = inLink || n.DataAtom == atom.A
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if !walk(c, inLink) {
				return false
			}
		}
		return true
	}
	return walk(doc, false) && linked
}

var blockTag = regexp.MustCompile(`(?i)<\s*(?:br|/p|/li|/div)\s*/?>`)

// stripTagsToLines turns block-ending tags into line breaks, so an
// instructions string written as HTML splits into steps.
func stripTagsToLines(s string) string {
	return blockTag.ReplaceAllString(s, "\n")
}
