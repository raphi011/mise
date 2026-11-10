package ai

import (
	"regexp"
	"strings"

	"github.com/raphi011/mise/internal/groceries"
)

// ingredientName is what an ingredient line names once its amount is
// gone: "sunflower or vegetable oil, plus extra for frying" names
// sunflower oil or vegetable oil.
type ingredientName struct {
	// Phrase is the name without notes: "sunflower or vegetable oil".
	Phrase string
	// Options are the names the line accepts, in the order written. An
	// elided first alternative is listed both completed with the last
	// one's head noun and as written: "sunflower oil", "sunflower",
	// "vegetable oil".
	Options []string
	// Primary counts the options of the first alternative, which the line
	// asks for; an item named like a later one is only a probable match.
	Primary int
	// New names the item to create when none matches: the first
	// alternative, or the last one when the first may be elided
	// ("sunflower" of "sunflower or vegetable oil"), since the last is
	// always written out in full.
	New string
}

// primary reports whether an item is named like the first alternative.
func (n ingredientName) primary(item string) bool {
	key := groceries.NameKey(item)
	for _, o := range n.Options[:n.Primary] {
		if groceries.NameKey(o) == key {
			return true
		}
	}
	return false
}

var parenthetical = regexp.MustCompile(`\s*\([^)]*\)`)

// noteWords start the note that follows a name: "oil plus extra for
// frying", "salt to taste", "Öl zum Braten". Only words after the first
// count, so a name is never empty.
var noteWords = map[string]bool{
	"plus": true, "for": true, "to": true, "at": true, "if": true, "such": true,
	"e.g.": true, "optional": true, "optionally": true, "divided": true,
	"zum": true, "zur": true, "für": true, "nach": true, "falls": true,
	"z.b.": true, "optional.": true, "evtl.": true,
	"+": true, "-": true, "–": true, "—": true,
}

// orWords separate alternatives: "butter or margarine", "Öl oder Butter".
var orWords = map[string]bool{"or": true, "oder": true, "/": true}

// parseIngredientName reads the name part of an ingredient line, what
// follows its amount.
func parseIngredientName(s string) ingredientName {
	s = parenthetical.ReplaceAllString(s, "")
	if i := strings.IndexAny(s, ",;"); i >= 0 {
		s = s[:i]
	}
	words := strings.Fields(s)
	for i, w := range words {
		if i > 0 && noteWords[strings.ToLower(w)] {
			words = words[:i]
			break
		}
	}
	var alts [][]string
	start := 0
	for i, w := range words {
		if orWords[strings.ToLower(w)] {
			if i > start {
				alts = append(alts, words[start:i])
			}
			start = i + 1
		}
	}
	if start < len(words) {
		alts = append(alts, words[start:])
	}
	n := ingredientName{Phrase: cleanName(strings.Join(words, " "))}
	if len(alts) == 0 || n.Phrase == "" {
		return ingredientName{}
	}
	last := alts[len(alts)-1]
	for i, a := range alts {
		raw := strings.Join(a, " ")
		name := cleanName(raw)
		elided := i < len(alts)-1 && len(a) == 1 && len(last) > 1
		if strings.HasSuffix(raw, "-") {
			// "Sonnenblumen- oder Rapsöl": the rest of the word is unknown.
			if i == 0 {
				n.New = cleanName(strings.Join(last, " "))
			}
			continue
		}
		if elided {
			n.Options = append(n.Options, cleanName(name+" "+strings.Join(last[1:], " ")))
		}
		n.Options = append(n.Options, name)
		if i == 0 {
			n.Primary = len(n.Options)
			n.New = name
			if elided {
				n.New = cleanName(strings.Join(last, " "))
			}
		}
	}
	if len(n.Options) == 0 {
		n.Options = []string{n.New}
	}
	return n
}

// cleanName trims list marks and stray punctuation and caps the length.
func cleanName(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "*-–•.:"))
	return truncate(s, maxItemName)
}
