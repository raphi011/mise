package catalog

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// TestCatalogParity: both languages have the same keys (load fails
// otherwise), keys look right, categories and icons exist, and no name or
// alias is claimed twice in a language, which would make Lookup ambiguous.
func TestCatalogParity(t *testing.T) {
	ensure()
	en := For("en").Entries()
	de := For("de").Entries()
	if len(en) != len(de) {
		t.Fatalf("%d English entries, %d German", len(en), len(de))
	}
	usedIcons := map[string]bool{}
	for i, e := range en {
		if de[i].Key != e.Key {
			t.Errorf("entry %d: key %q in en, %q in de", i, e.Key, de[i].Key)
		}
		if !keyPattern.MatchString(e.Key) {
			t.Errorf("key %q does not match %s", e.Key, keyPattern)
		}
		if !slices.Contains(Categories, e.Category) {
			t.Errorf("%s: unknown category %q", e.Key, e.Category)
		}
		if e.Icon != "" {
			usedIcons[e.Icon] = true
			if !HasIcon(e.Icon) {
				t.Errorf("%s: icon %q has no file in icons/", e.Key, e.Icon)
			}
		}
	}
	for _, k := range IconKeys() {
		if !usedIcons[k] {
			t.Errorf("icon %q is not used by any entry", k)
		}
	}
	for _, lang := range Languages {
		owner := map[string]string{}
		for _, e := range For(lang).Entries() {
			for _, s := range append([]string{e.Name}, e.Aliases...) {
				k := NameKey(s)
				if k == "" {
					t.Errorf("%s/%s: empty name", lang, e.Key)
				}
				if prev, ok := owner[k]; ok && prev != e.Key {
					t.Errorf("%s: %q is a name of both %s and %s", lang, s, prev, e.Key)
				}
				owner[k] = e.Key
			}
		}
	}
}

// TestCatalogKeysAppendOnly: a key may be added but never disappear
// without a retired entry, because items and other data refer to it.
func TestCatalogKeysAppendOnly(t *testing.T) {
	ensure()
	b, err := os.ReadFile("testdata/keys.lock")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range For("en").Entries() {
		have[e.Key] = true
	}
	for _, k := range strings.Fields(string(b)) {
		if have[k] {
			continue
		}
		if _, ok := retired[k]; !ok {
			t.Errorf("key %q was removed; move it to data/retired.json with its replacement", k)
		}
	}
	for k := range have {
		if !strings.Contains("\n"+string(b), "\n"+k+"\n") {
			t.Errorf("key %q is new: add it to testdata/keys.lock (keep the file sorted)", k)
		}
	}
	for k, to := range retired {
		if to != "" && !have[to] {
			t.Errorf("retired key %q is replaced by unknown %q", k, to)
		}
	}
}

func TestLookup(t *testing.T) {
	de, en := For("de-AT"), For("fr")
	if e, ok := de.Lookup("  ZITRONEN "); !ok || e.Key != "lemon" || e.Name != "Zitrone" {
		t.Errorf("de lookup: %+v %v", e, ok)
	}
	if e, ok := en.Lookup("Lemons"); !ok || e.Key != "lemon" {
		t.Errorf("fallback to English: %+v %v", e, ok)
	}
	if e, ok := en.Within("2 large garlic cloves, minced"); !ok || e.Key != "garlic" {
		t.Errorf("within: %+v %v", e, ok)
	}
	if e, ok := en.Within("fresh red onions"); !ok || e.Key != "red_onion" {
		t.Errorf("longest name wins: %+v %v", e, ok)
	}
	if _, ok := en.Lookup("unobtainium"); ok {
		t.Error("unknown name matched")
	}
}

func TestSearch(t *testing.T) {
	got := For("en").Search("lem", 5, nil)
	if len(got) == 0 || got[0].Entry.Key != "lemon" || got[0].Match != MatchPrefix {
		t.Fatalf("search lem: %+v", got)
	}
	skipped := For("en").Search("lem", 5, func(k string) bool { return k == "lemon" })
	for _, h := range skipped {
		if h.Entry.Key == "lemon" {
			t.Error("skipped entry returned")
		}
	}
}

func TestRetiredKeysResolve(t *testing.T) {
	ensure()
	old := retired
	t.Cleanup(func() { retired = old })
	retired = map[string]string{"zitrone_old": "lemon", "gone": ""}
	if e, ok := For("en").Entry("zitrone_old"); !ok || e.Key != "lemon" {
		t.Errorf("retired key: %+v %v", e, ok)
	}
	if _, ok := For("en").Entry("gone"); ok {
		t.Error("key retired without replacement resolved")
	}
	if Icon("zitrone_old") != "lemon" {
		t.Errorf("icon of retired key = %q", Icon("zitrone_old"))
	}
}

func TestIconSVG(t *testing.T) {
	svg := IconSVG("lemon")
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "currentColor") {
		t.Errorf("lemon svg: %.60s", svg)
	}
	if IconSVG("nope") != "" || HasIcon("nope") {
		t.Error("unknown icon")
	}
}
