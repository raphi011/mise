package ai

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Recipe is a recipe as read from a source (JSON-LD or the model), before
// its ingredients are matched to items.
type Recipe struct {
	Name        string
	Description string
	PrepMin     int // 0: unknown
	CookMin     int
	Servings    int
	SourceURL   string
	ImageURL    string
	Ingredients []string
	Steps       []Step
	// Keywords are tag candidates (keywords, category, cuisine).
	Keywords []string
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
		for _, t := range strs(v["@type"]) {
			if t == "Recipe" || strings.HasSuffix(t, "/Recipe") {
				return v
			}
		}
		for _, key := range []string{"@graph", "mainEntity", "mainEntityOfPage"} {
			if r := findRecipe(v[key], depth+1); r != nil {
				return r
			}
		}
	}
	return nil
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
			if line = cleanText(line); line != "" {
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
		text := cleanText(str(v["text"]))
		if text == "" {
			text = cleanText(str(v["name"]))
		}
		if text != "" {
			out = append(out, Step{Text: text})
		}
	}
	return out
}

var blockTag = regexp.MustCompile(`(?i)<\s*(?:br|/p|/li|/div)\s*/?>`)

// stripTagsToLines turns block-ending tags into line breaks, so an
// instructions string written as HTML splits into steps.
func stripTagsToLines(s string) string {
	return blockTag.ReplaceAllString(s, "\n")
}
