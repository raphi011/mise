package recipes

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"

	"github.com/raphi011/mise/internal/groceries"
)

// Part is a piece of a step's text. Ingredient is -1 for plain text, else
// the index of the ingredient the piece mentions.
type Part struct {
	Text       string
	Ingredient int
}

// pluralSuffixes are endings a mention may add to or drop from an item name
// ("onion" in "Spring onions", "Tomaten" for "Tomate").
var pluralSuffixes = []string{"es", "en", "s", "n", "e"}

// Highlight splits a step into plain text and ingredient mentions. Each
// ingredient is highlighted on its first mention in the steps only: seen
// carries which ingredients earlier steps mentioned and is updated. A
// mention is the item's full name, or its last word when that is at least
// four letters and no other ingredient ends in it ("tofu" for "Firm tofu"),
// at word boundaries, ignoring case and a plural ending.
func Highlight(step string, ings []Ingredient, seen map[int]bool) []Part {
	type cand struct {
		ing  int
		name string // case-folded
	}
	lastWords := map[string]int{}
	for _, ing := range ings {
		if w := lastWord(ing.ItemName); w != "" {
			lastWords[w]++
		}
	}
	var cands []cand
	for i, ing := range ings {
		if seen[i] {
			continue
		}
		full := groceries.NameKey(ing.ItemName)
		if full == "" {
			continue
		}
		cands = append(cands, cand{i, full})
		if w := lastWord(ing.ItemName); w != full && utf8.RuneCountInString(w) >= 4 && lastWords[w] == 1 {
			cands = append(cands, cand{i, w})
		}
	}
	// Longer names first, so "soy sauce" wins over "sauce".
	slices.SortStableFunc(cands, func(a, b cand) int { return len(b.name) - len(a.name) })

	ft := foldText(step)
	var parts []Part
	pos := 0
	for pos < len(step) {
		best, bestEnd, bestIng := -1, 0, -1
		for _, c := range cands {
			if seen[c.ing] {
				continue
			}
			start, end := ft.find(c.name, pos)
			if start >= 0 && (best < 0 || start < best || (start == best && end > bestEnd)) {
				best, bestEnd, bestIng = start, end, c.ing
			}
		}
		if best < 0 {
			break
		}
		if best > pos {
			parts = append(parts, Part{Text: step[pos:best], Ingredient: -1})
		}
		parts = append(parts, Part{Text: step[best:bestEnd], Ingredient: bestIng})
		seen[bestIng] = true
		pos = bestEnd
	}
	if pos < len(step) {
		parts = append(parts, Part{Text: step[pos:], Ingredient: -1})
	}
	return parts
}

func lastWord(name string) string {
	f := strings.Fields(groceries.NameKey(name))
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// foldedText is a step case-folded rune by rune, with the byte offset in
// the original of every folded byte, so matches map back.
type foldedText struct {
	orig   string
	folded string
	at     []int // at[i] is the original offset of folded byte i; at[len] = len(orig)
}

func foldText(s string) foldedText {
	fold := cases.Fold()
	var b strings.Builder
	at := make([]int, 0, len(s)+1)
	for i, r := range s {
		f := fold.String(string(r))
		b.WriteString(f)
		for range len(f) {
			at = append(at, i)
		}
	}
	at = append(at, len(s))
	return foldedText{orig: s, folded: b.String(), at: at}
}

// find finds name (case-folded) at or after original byte offset from, at
// word boundaries and allowing a plural ending on either side. It returns
// byte offsets into the original text, or -1.
func (t foldedText) find(name string, from int) (int, int) {
	variants := []string{name}
	for _, suf := range pluralSuffixes {
		if strings.HasSuffix(name, suf) && len(name) > len(suf)+2 {
			variants = append(variants, strings.TrimSuffix(name, suf))
		}
	}
	f := t.folded
	for i := 0; i < len(f); {
		_, size := utf8.DecodeRuneInString(f[i:])
		if t.at[i] < from || (i > 0 && isWordRune(lastRune(f[:i]))) {
			i += size
			continue
		}
		for _, v := range variants {
			if !strings.HasPrefix(f[i:], v) {
				continue
			}
			end := i + len(v)
			if end == len(f) || !isWordRune(firstRune(f[end:])) {
				return t.at[i], t.at[end]
			}
			for _, suf := range pluralSuffixes {
				e := end + len(suf)
				if strings.HasPrefix(f[end:], suf) && (e == len(f) || !isWordRune(firstRune(f[e:]))) {
					return t.at[i], t.at[e]
				}
			}
		}
		i += size
	}
	return -1, -1
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}
