// Package catalog is the embedded catalogue of default ingredients: a
// stable key per ingredient, its name and aliases in each language, a
// default category and an icon. Households link items to entries through
// items.catalog_key; the catalogue itself is never stored per household
// (docs/plan.md, Default ingredients).
//
// Keys are the contract with every other feature that hangs data on an
// ingredient. They never change and are never reused: a key that goes
// away moves to data/retired.json.
package catalog

import (
	"cmp"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

//go:embed data/*.json icons/*.svg
var files embed.FS

// Categories are the catalogue's category keys in default aisle order.
var Categories = []string{"produce", "bakery", "meat_fish", "fridge", "frozen", "pantry", "spices", "drinks", "household"}

// Entry is one ingredient in one language.
type Entry struct {
	Key string
	// Name is the name in the catalogue's language.
	Name string
	// Aliases are other names that find the entry (plurals, synonyms).
	Aliases []string
	// Category is one of Categories.
	Category string
	// Icon is the key of an embedded icon, or "" for none.
	Icon string
}

// Catalog is the catalogue in one language.
type Catalog struct {
	entries []Entry
	byKey   map[string]int
	// byName maps NameKey of every name and alias to an entry index.
	byName map[string]int
}

// NameKey returns the case-folded, space-normalised form of a name used for
// uniqueness and lookup, so "Äpfel", "äpfel " and "ÄPFEL" are one name.
func NameKey(name string) string {
	return norm.NFC.String(cases.Fold().String(strings.Join(strings.FieldsFunc(name, unicode.IsSpace), " ")))
}

type baseEntry struct {
	Key      string `json:"key"`
	Category string `json:"category"`
	Icon     string `json:"icon"`
}

type names struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

var (
	once    sync.Once
	loadErr error
	byLang  map[string]*Catalog
	retired map[string]string
	icons   map[string]string
	// nutrition is data/nutrition.json by entry key.
	nutrition map[string]Nutrition
)

// Languages are the catalogue's languages; the first is the fallback.
var Languages = []string{"en", "de"}

func load() {
	var base []baseEntry
	if loadErr = readJSON("data/entries.json", &base); loadErr != nil {
		return
	}
	if loadErr = readJSON("data/retired.json", &retired); loadErr != nil {
		return
	}
	if loadErr = readJSON("data/nutrition.json", &nutrition); loadErr != nil {
		return
	}
	byLang = map[string]*Catalog{}
	for _, lang := range Languages {
		var n map[string]names
		if loadErr = readJSON("data/names."+lang+".json", &n); loadErr != nil {
			return
		}
		c := &Catalog{byKey: map[string]int{}, byName: map[string]int{}}
		for _, b := range base {
			nm, ok := n[b.Key]
			if !ok {
				loadErr = fmt.Errorf("catalog: %s has no %s name", b.Key, lang)
				return
			}
			i := len(c.entries)
			c.entries = append(c.entries, Entry{Key: b.Key, Name: nm.Name, Aliases: nm.Aliases, Category: b.Category, Icon: b.Icon})
			c.byKey[b.Key] = i
			for _, s := range append([]string{nm.Name}, nm.Aliases...) {
				// The first entry to claim a name keeps it; the parity test
				// rejects clashes.
				if _, taken := c.byName[NameKey(s)]; !taken {
					c.byName[NameKey(s)] = i
				}
			}
		}
		byLang[lang] = c
	}
	icons = map[string]string{}
	dir, err := files.ReadDir("icons")
	if err != nil {
		loadErr = err
		return
	}
	for _, f := range dir {
		b, err := files.ReadFile("icons/" + f.Name())
		if err != nil {
			loadErr = err
			return
		}
		icons[strings.TrimSuffix(f.Name(), ".svg")] = string(b)
	}
}

func readJSON(name string, v any) error {
	b, err := files.ReadFile(name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("catalog: %s: %w", name, err)
	}
	return nil
}

func ensure() {
	once.Do(load)
	if loadErr != nil {
		// The data is embedded, so this is a build error caught by the
		// package tests, never a runtime condition.
		panic(loadErr)
	}
}

// For returns the catalogue in the language of tag or code ("de", "de-AT");
// languages without one get English.
func For(lang string) *Catalog {
	ensure()
	if t, err := language.Parse(lang); err == nil {
		base, _ := t.Base()
		if c, ok := byLang[base.String()]; ok {
			return c
		}
	}
	return byLang[Languages[0]]
}

// Entries returns every entry. The caller must not modify it.
func (c *Catalog) Entries() []Entry { return c.entries }

// Entry returns the entry for key, following retired keys to their
// replacement.
func (c *Catalog) Entry(key string) (Entry, bool) {
	for range 4 {
		if i, ok := c.byKey[key]; ok {
			return c.entries[i], true
		}
		next, ok := retired[key]
		if !ok || next == "" {
			return Entry{}, false
		}
		key = next
	}
	return Entry{}, false
}

// Lookup returns the entry whose name or alias is phrase.
func (c *Catalog) Lookup(phrase string) (Entry, bool) {
	i, ok := c.byName[NameKey(phrase)]
	if !ok {
		return Entry{}, false
	}
	return c.entries[i], true
}

// Within returns the entry whose name or alias appears as whole words in
// text ("Tofu" in "extra-firm tofu"), the longest one first.
func (c *Catalog) Within(text string) (Entry, bool) {
	padded := " " + wordKey(NameKey(text)) + " "
	best, bestLen := -1, 0
	for name, i := range c.byName {
		k := wordKey(name)
		if k == "" || !strings.Contains(padded, " "+k+" ") {
			continue
		}
		if len(k) > bestLen || (len(k) == bestLen && i < best) {
			best, bestLen = i, len(k)
		}
	}
	if best < 0 {
		return Entry{}, false
	}
	return c.entries[best], true
}

// Match says how a name matches what was typed, best first.
type Match int

// Matches.
const (
	MatchExact Match = iota
	MatchPrefix
	MatchWord
	MatchContains
)

// Hit is a search result.
type Hit struct {
	Entry Entry
	Match Match
}

// Search returns the entries whose name or alias contains what was typed,
// best match first, then by name. skip lists keys to leave out (entries the
// household already has as items).
func (c *Catalog) Search(typed string, limit int, skip func(key string) bool) []Hit {
	key := NameKey(typed)
	if key == "" {
		return nil
	}
	best := map[int]Match{}
	for name, i := range c.byName {
		if !strings.Contains(name, key) {
			continue
		}
		m := MatchContains
		switch {
		case name == key:
			m = MatchExact
		case strings.HasPrefix(name, key):
			m = MatchPrefix
		case strings.Contains(name, " "+key):
			m = MatchWord
		}
		if cur, ok := best[i]; !ok || m < cur {
			best[i] = m
		}
	}
	var out []Hit
	for i, m := range best {
		if skip != nil && skip(c.entries[i].Key) {
			continue
		}
		out = append(out, Hit{Entry: c.entries[i], Match: m})
	}
	slices.SortFunc(out, func(a, b Hit) int {
		return cmp.Or(cmp.Compare(a.Match, b.Match), cmp.Compare(len(a.Entry.Name), len(b.Entry.Name)), strings.Compare(a.Entry.Key, b.Entry.Key))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// wordKey turns punctuation into spaces so words can be compared.
func wordKey(key string) string {
	return strings.Join(strings.FieldsFunc(key, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// Icon returns the catalogue icon key of an entry key ("" when the key is
// unknown or the entry has no icon). Icons do not depend on the language.
func Icon(key string) string {
	if key == "" {
		return ""
	}
	e, ok := For(Languages[0]).Entry(key)
	if !ok {
		return ""
	}
	return e.Icon
}

// HasIcon reports whether an icon key exists.
func HasIcon(icon string) bool {
	ensure()
	_, ok := icons[icon]
	return ok
}

// IconKeys returns the available icon keys in alphabetical order.
func IconKeys() []string {
	ensure()
	keys := make([]string, 0, len(icons))
	for k := range icons {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// IconSVG returns the SVG of an icon: a 24 px viewBox drawn in
// currentColor, so it takes the colour of the element around it. It is
// embedded markup, safe to render unescaped. Unknown keys return "".
func IconSVG(icon string) string {
	ensure()
	return icons[icon]
}
