package groceries

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"
)

// Amount is how much of an item to get. Text is what the user typed, or a
// formatted sum after merging; it may be empty. Quantity and Unit are set
// when Text is a number with an optional known unit ("500 g", "2", "½ Bund");
// free text such as "1 thumb" has no Quantity. Scaling and merging act on
// Quantity only.
type Amount struct {
	Quantity *float64
	// Unit is a canonical unit key (see Units) or empty.
	Unit string
	Text string
}

// IsZero reports whether no amount was given.
func (a Amount) IsZero() bool { return a.Text == "" && a.Quantity == nil }

// Structured reports whether the amount has a parsed quantity.
func (a Amount) Structured() bool { return a.Quantity != nil }

// unitDef is one canonical unit: its aliases (lower case, in English and
// German) and its labels for display.
type unitDef struct {
	key     string
	aliases []string
	// labels per language base: singular, plural.
	en, de [2]string
}

// countable lists the units that also name things to buy on their own:
// "2 cans" or "3 Flaschen" with no other text means two cans, not an amount
// of nothing. Measures and abbreviations ("2 l", "1 Stk.") never do.
var countable = map[string]bool{"can": true, "bottle": true, "bunch": true, "pack": true, "clove": true, "slice": true}

// Units are the units the parser knows, by canonical key.
var Units = []unitDef{
	{"g", []string{"g", "gr", "gram", "grams", "gramm"}, [2]string{"g", "g"}, [2]string{"g", "g"}},
	{"kg", []string{"kg", "kilo", "kilos", "kilogram", "kilograms", "kilogramm"}, [2]string{"kg", "kg"}, [2]string{"kg", "kg"}},
	{"mg", []string{"mg"}, [2]string{"mg", "mg"}, [2]string{"mg", "mg"}},
	{"l", []string{"l", "liter", "litre", "liters", "litres"}, [2]string{"l", "l"}, [2]string{"l", "l"}},
	{"ml", []string{"ml"}, [2]string{"ml", "ml"}, [2]string{"ml", "ml"}},
	{"cl", []string{"cl"}, [2]string{"cl", "cl"}, [2]string{"cl", "cl"}},
	{"dl", []string{"dl"}, [2]string{"dl", "dl"}, [2]string{"dl", "dl"}},
	{"tsp", []string{"tsp", "teaspoon", "teaspoons", "tl", "teelöffel"}, [2]string{"tsp", "tsp"}, [2]string{"TL", "TL"}},
	{"tbsp", []string{"tbsp", "tablespoon", "tablespoons", "el", "esslöffel"}, [2]string{"tbsp", "tbsp"}, [2]string{"EL", "EL"}},
	{"cup", []string{"cup", "cups", "tasse", "tassen"}, [2]string{"cup", "cups"}, [2]string{"Tasse", "Tassen"}},
	{"pinch", []string{"pinch", "pinches", "prise", "prisen"}, [2]string{"pinch", "pinches"}, [2]string{"Prise", "Prisen"}},
	{"bunch", []string{"bunch", "bunches", "bund"}, [2]string{"bunch", "bunches"}, [2]string{"Bund", "Bund"}},
	{"piece", []string{"pc", "pcs", "piece", "pieces", "stk", "stück"}, [2]string{"pc", "pcs"}, [2]string{"Stk.", "Stk."}},
	{"pack", []string{"pack", "packs", "package", "packages", "pkg", "pck", "pkt", "packung", "packungen", "päckchen"}, [2]string{"pack", "packs"}, [2]string{"Pck.", "Pck."}},
	{"can", []string{"can", "cans", "dose", "dosen"}, [2]string{"can", "cans"}, [2]string{"Dose", "Dosen"}},
	{"bottle", []string{"bottle", "bottles", "flasche", "flaschen"}, [2]string{"bottle", "bottles"}, [2]string{"Flasche", "Flaschen"}},
	{"clove", []string{"clove", "cloves", "zehe", "zehen"}, [2]string{"clove", "cloves"}, [2]string{"Zehe", "Zehen"}},
	{"slice", []string{"slice", "slices", "scheibe", "scheiben"}, [2]string{"slice", "slices"}, [2]string{"Scheibe", "Scheiben"}},
	{"oz", []string{"oz"}, [2]string{"oz", "oz"}, [2]string{"oz", "oz"}},
	{"lb", []string{"lb", "lbs"}, [2]string{"lb", "lb"}, [2]string{"lb", "lb"}},
}

var (
	unitByAlias = map[string]*unitDef{}
	unitByKey   = map[string]*unitDef{}
)

func init() {
	for i := range Units {
		u := &Units[i]
		unitByKey[u.key] = u
		for _, a := range u.aliases {
			unitByAlias[a] = u
		}
	}
}

// lookupUnit finds a unit by what the user typed, ignoring case and a
// trailing full stop ("Stk.", "Pck.").
func lookupUnit(word string) (*unitDef, bool) {
	w := strings.ToLower(strings.TrimSuffix(word, "."))
	u, ok := unitByAlias[w]
	return u, ok
}

// ValidUnit reports whether key is a canonical unit key.
func ValidUnit(key string) bool {
	_, ok := unitByKey[key]
	return ok
}

// vulgar maps Unicode fraction characters to their values.
var vulgar = map[rune]float64{
	'½': 1.0 / 2, '⅓': 1.0 / 3, '⅔': 2.0 / 3, '¼': 1.0 / 4, '¾': 3.0 / 4,
	'⅕': 1.0 / 5, '⅙': 1.0 / 6, '⅛': 1.0 / 8, '⅜': 3.0 / 8, '⅝': 5.0 / 8, '⅞': 7.0 / 8,
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// decimalSeparator returns the decimal separator of the language. The other
// separator is read as a thousands separator before exactly three digits
// ("1.000 g" in German, "1,000 g" in English) and as a decimal separator
// otherwise, so "1,5 kg" works in either language.
func decimalSeparator(lang language.Tag) byte {
	if base, _ := lang.Base(); base.String() == "de" {
		return ','
	}
	return '.'
}

// numberPrefix parses a number at the start of s: "2", "1,5", "1.5",
// "1.000", "1/2", "½", "1½". It returns the value, the number of bytes
// consumed and whether the number was a fraction on its own.
func numberPrefix(s string, lang language.Tag) (v float64, n int, fraction bool, ok bool) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == 0 {
		r, size := utf8.DecodeRuneInString(s)
		if f, ok := vulgar[r]; ok {
			return f, size, true, true
		}
		return 0, 0, false, false
	}
	whole, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, 0, false, false
	}

	if i < len(s) && s[i] == '/' {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		if j > i+1 {
			den, err := strconv.ParseFloat(s[i+1:j], 64)
			if err == nil && den != 0 {
				return whole / den, j, true, true
			}
		}
		return whole, i, false, true
	}

	if i < len(s) && (s[i] == ',' || s[i] == '.') {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		frac := s[i+1 : j]
		if frac != "" {
			if s[i] != decimalSeparator(lang) && len(frac) == 3 {
				g, _ := strconv.ParseFloat(frac, 64)
				return whole*1000 + g, j, false, true
			}
			f, err := strconv.ParseFloat("0."+frac, 64)
			if err == nil {
				return whole + f, j, false, true
			}
		}
		return whole, i, false, true
	}

	if r, size := utf8.DecodeRuneInString(s[i:]); size > 0 {
		if f, ok := vulgar[r]; ok {
			return whole + f, i + size, false, true
		}
	}
	return whole, i, false, true
}

// firstWord splits s at the first space.
func firstWord(s string) (word, rest string) {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// leadingAmount parses an amount at the start of s, which has single spaces.
// It returns the amount and the remaining text.
func leadingAmount(s string, lang language.Tag) (Amount, string, bool) {
	v, n, fraction, ok := numberPrefix(s, lang)
	if !ok {
		return Amount{}, "", false
	}
	pos := n
	// Mixed numbers: "1 1/2", "1 ½".
	if !fraction && strings.HasPrefix(s[pos:], " ") {
		t := s[pos+1:]
		if f, m, isFrac, ok := numberPrefix(t, lang); ok && isFrac && (m == len(t) || t[m] == ' ') {
			v += f
			pos += 1 + m
		}
	}

	after := s[pos:]
	trimmed := strings.TrimPrefix(after, " ")
	spaced := len(trimmed) < len(after)
	word, rest := firstWord(trimmed)
	unit := ""
	end := pos
	switch u, ok := lookupUnit(word); {
	case word == "":
	case ok:
		unit = u.key
		end = len(s) - len(trimmed) + len(word)
	case word == "x" || word == "×":
		// "2x milk", "2 x milk": a count.
		end = len(s) - len(trimmed) + len(word)
	case !spaced:
		// "7up" is a name, not an amount.
		return Amount{}, "", false
	default:
		rest = trimmed
	}
	if unit == "" && end == pos {
		rest = trimmed
	}
	return Amount{Quantity: &v, Unit: unit, Text: strings.TrimSpace(s[:end])}, strings.TrimSpace(rest), true
}

// wholeAmount parses s as an amount and nothing else.
func wholeAmount(s string, lang language.Tag) (Amount, bool) {
	a, rest, ok := leadingAmount(s, lang)
	if !ok || rest != "" {
		return Amount{}, false
	}
	return a, true
}

// normalizeSpace trims s and collapses runs of white space to one space.
func normalizeSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// ParseEntry splits what was typed into the add field into an amount and an
// item name: "2 lemons", "500g cherry tomatoes", "1,5 l Milch",
// "½ Bund Petersilie", "Lemons 2". The language decides the decimal
// separator. Without a recognisable amount the whole input is the name.
func ParseEntry(input string, lang language.Tag) (Amount, string) {
	s := normalizeSpace(input)
	if s == "" {
		return Amount{}, ""
	}
	if a, name, ok := leadingAmount(s, lang); ok {
		if name == "" && countable[a.Unit] {
			// "2 cans": the unit word was the name. "2cans", like "500g"
			// or "2 l" alone, has none.
			if i := strings.LastIndexByte(a.Text, ' '); i >= 0 {
				a.Text, name, a.Unit = a.Text[:i], a.Text[i+1:], ""
			}
		}
		return a, name
	}
	// Trailing amount: "Lemons 2", "Milch 1 l", "Tomaten 500g".
	fields := strings.Split(s, " ")
	for k := min(2, len(fields)-1); k >= 1; k-- {
		tail := strings.Join(fields[len(fields)-k:], " ")
		if a, ok := wholeAmount(tail, lang); ok && !partOfName(a) {
			return a, strings.Join(fields[:len(fields)-k], " ")
		}
	}
	return Amount{}, s
}

// partOfName reports whether a trailing bare number of three or more digits
// belongs to the name, like the flour type in "Mehl 405" or "Weizenmehl Type
// 550". Trailing counts are small ("Eier 10"); larger trailing amounts need a
// unit ("Tomaten 500 g").
func partOfName(a Amount) bool {
	if a.Unit != "" || len(a.Text) < 3 {
		return false
	}
	for i := range len(a.Text) {
		if !isDigit(a.Text[i]) {
			return false
		}
	}
	return true
}

// ParseAmount parses the amount field of the edit sheet. Text that does not
// parse is kept as free text without a quantity.
func ParseAmount(input string, lang language.Tag) Amount {
	s := normalizeSpace(input)
	if s == "" {
		return Amount{}
	}
	if a, ok := wholeAmount(s, lang); ok {
		a.Text = s
		return a
	}
	return Amount{Text: s}
}

// FormatQuantity formats q for display in the language, with at most three
// decimals.
func FormatQuantity(q float64, lang language.Tag) string {
	q = math.Round(q*1000) / 1000
	s := strconv.FormatFloat(q, 'f', -1, 64)
	if decimalSeparator(lang) == ',' {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// FormatAmount formats a quantity with a canonical unit in the language,
// e.g. "750 g" or "2 EL".
func FormatAmount(q float64, unit string, lang language.Tag) string {
	s := FormatQuantity(q, lang)
	u, ok := unitByKey[unit]
	if !ok {
		return s
	}
	labels := u.en
	if decimalSeparator(lang) == ',' {
		labels = u.de
	}
	label := labels[1]
	if q == 1 {
		label = labels[0]
	}
	return s + " " + label
}

// merge adds b to a when both are structured in the same unit. ok is false
// when the amounts cannot be combined.
func merge(a, b Amount, lang language.Tag) (Amount, bool) {
	switch {
	case b.IsZero():
		return a, true
	case a.IsZero():
		return b, true
	case a.Structured() && b.Structured() && a.Unit == b.Unit:
		sum := *a.Quantity + *b.Quantity
		return Amount{Quantity: &sum, Unit: a.Unit, Text: FormatAmount(sum, a.Unit, lang)}, true
	}
	return Amount{}, false
}
