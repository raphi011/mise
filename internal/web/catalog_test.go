package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/catalog"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestCatalogueOverHTTP: a new household gets catalogue suggestions with
// icons; picking one creates the item, and the edit sheet changes its icon.
func TestCatalogueOverHTTP(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	list := liveListRe.FindStringSubmatch(anna.get("/groceries").body)[1]

	res := anna.datastar(http.MethodGet, "/groceries/lists/"+list+"/suggest?datastar="+url.QueryEscape(`{"add":"lem"}`), nil)
	res.expect(t, "suggest", http.StatusOK)
	for _, want := range []string{"Lemon", "Catalogue", "Produce", `"_opts":["cat:lemon"`, `value="cat:lemon"`, "<svg"} {
		if !strings.Contains(res.body, want) {
			t.Errorf("suggestions lack %q:\n%s", want, res.body)
		}
	}

	anna.post("/groceries/lists/"+list+"/entries", url.Values{"text": {"2 lem"}, "pick": {"cat:lemon"}}).expect(t, "add from catalogue", http.StatusSeeOther)
	p := anna.get("/groceries").expect(t, "list", http.StatusOK)
	id := entryRe.FindStringSubmatch(p.body)
	if id == nil || !strings.Contains(p.body, "Check off: Lemon, 2") || !strings.Contains(p.body, lemonMarker) {
		t.Fatalf("list lacks the lemon with its icon:\n%.3000s", p.body)
	}

	edit := anna.get("/groceries/entries/"+id[1]+"/edit").expect(t, "edit", http.StatusOK).body
	for _, want := range []string{`name="icon" value="onion"`, `name="icon" value="none"`, "Catalogue icon", "No icon"} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit page lacks %q", want)
		}
	}
	catID := regexp.MustCompile(`name="category" value="([0-9a-f-]{36})" checked`).FindStringSubmatch(edit)
	if catID == nil {
		t.Fatalf("lemon is not filed under its catalogue category:\n%s", edit)
	}
	form := url.Values{"name": {"Lemon"}, "category": {catID[1]}, "list": {list}, "icon": {"onion"}}
	anna.post("/groceries/entries/"+id[1]+"/edit", form).expect(t, "save icon", http.StatusSeeOther)
	p = anna.get("/groceries").expect(t, "list after edit", http.StatusOK)
	if strings.Contains(p.body, lemonMarker) || !strings.Contains(p.body, onionMarker) {
		t.Error("the picked icon is not shown")
	}
	form.Set("icon", "nope")
	anna.post("/groceries/entries/"+id[1]+"/edit", form).expect(t, "unknown icon", http.StatusUnprocessableEntity)
}

// Fragments of the lemon and onion paths, which identify the icons in a page.
const (
	lemonMarker = "M16.3 5.3c.2-1.5"
	onionMarker = "M10.5 21.3l-.4 1"
)

// TestIconLabels: every icon has a translated name in both languages.
func TestIconLabels(t *testing.T) {
	for _, tag := range []language.Tag{language.English, language.German} {
		ctx := i18n.WithLocale(t.Context(), tag)
		for _, k := range catalog.IconKeys() {
			if key := i18n.Key("icon." + k); i18n.T(ctx, key) == string(key) {
				t.Errorf("%s: no translation of %s", tag, key)
			}
		}
	}
}
