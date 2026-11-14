package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdSettings: admins edit the household and pick the recipe
// sites; the search asks only those, and members see the household's
// language.
func TestHouseholdSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startImportServer(t, db)
		ss := withSites(t, s)
		anna, ben := household(t, srv)
		const path = "/settings/household"

		// Admins only.
		ben.get(path).expect(t, "member page", http.StatusForbidden)
		ben.post(path, url.Values{"household": {"Mine"}}).expect(t, "member save", http.StatusForbidden)
		if body := ben.get("/settings").body; strings.Contains(body, `href="/settings/household"`) {
			t.Error("member sees the household settings")
		}
		// By default the sites in the household's language (English) are on.
		if body := anna.get("/settings").body; !strings.Contains(body, `href="/settings/household"`) || !strings.Contains(body, "Recipe sites searched: 1") {
			t.Errorf("settings overview:\n%.6000s", body)
		}
		body := anna.get(path).expect(t, "page", http.StatusOK).body
		for _, want := range []string{
			`name="household" type="text" value="Home"`, `value="Europe/Vienna"`, `<option value="en" selected>English</option>`,
			`German <span class="lang-badge" aria-hidden="true">DE</span>`, `English <span class="lang-badge" aria-hidden="true">EN</span>`,
			`name="sites" value="ichkoche.at">`, `name="sites" value="kochbar.test" checked>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("page lacks %q", want)
			}
		}
		if !inOrder(body, "German", "ichkoche.at", "English", "kochbar.test") {
			t.Error("sites not grouped by language")
		}
		if t.Failed() {
			t.Fatalf("page:\n%.8000s", body)
		}

		// Invalid input keeps what was submitted, sites included, and saves
		// nothing.
		res := anna.post(path, url.Values{"household": {" "}, "timezone": {"Mars/Base"}, "week_start": {"mon"}, "language": {"en"}, "sites": {"ichkoche.at"}})
		res.expect(t, "invalid", http.StatusUnprocessableEntity)
		for _, want := range []string{"Use a time zone name like Europe/Vienna.", "Fill this in.", `name="sites" value="ichkoche.at" checked>`, `name="sites" value="kochbar.test">`} {
			if !strings.Contains(res.body, want) {
				t.Errorf("invalid save lacks %q:\n%.6000s", want, res.body)
			}
		}
		if body := anna.get(path).body; !strings.Contains(body, `name="household" type="text" value="Home"`) || !strings.Contains(body, `value="kochbar.test" checked>`) {
			t.Error("invalid save changed the household")
		}

		// A German household with the English site on and the German one
		// off: members without a language of their own now see German.
		anna.post(path, url.Values{"household": {"Casa"}, "timezone": {"Europe/Berlin"}, "week_start": {"sun"}, "language": {"de"}, "sites": {"kochbar.test"}}).
			expect(t, "save", http.StatusSeeOther, path+"?done=saved")
		body = anna.get(path + "?done=saved").body
		for _, want := range []string{"Gespeichert", `name="household" type="text" value="Casa"`, `value="Europe/Berlin"`, `<option value="sun" selected>`,
			`<option value="de" selected>Deutsch</option>`, `name="sites" value="ichkoche.at">`, `name="sites" value="kochbar.test" checked>`} {
			if !strings.Contains(body, want) {
				t.Errorf("saved page lacks %q", want)
			}
		}
		if body := ben.get("/recipes").body; !strings.Contains(body, `lang="de"`) {
			t.Error("member does not see the household language")
		}

		// The search asks only the household's sites; a filter for a site
		// that is off searches the others.
		for _, q := range []string{"tofu", "tofu&site=ichkoche.at"} {
			body = anna.get("/recipes/search?q=" + q).body
			if !strings.Contains(body, "Tofu curry") || strings.Contains(body, "Crispy tofu") || strings.Contains(body, "chip--on") {
				t.Errorf("search %s:\n%.6000s", q, body)
			}
		}
		if ss.ichHits.Load() != 0 {
			t.Errorf("searched a site that is off %d times", ss.ichHits.Load())
		}

		// With every site off the search says so; admins get a link to
		// turn sites on.
		anna.post(path, url.Values{"household": {"Casa"}, "timezone": {"Europe/Berlin"}, "week_start": {"sun"}, "language": {"de"}}).
			expect(t, "all off", http.StatusSeeOther)
		before := ss.searches()
		if body := anna.get("/recipes/search?q=tofu").body; !strings.Contains(body, "keine Rezeptseiten eingeschaltet") ||
			!strings.Contains(body, `href="/settings/household#sites"`) {
			t.Errorf("no sites, admin:\n%.6000s", body)
		}
		if body := ben.get("/recipes/search?q=tofu").body; !strings.Contains(body, "keine Rezeptseiten eingeschaltet") ||
			strings.Contains(body, `href="/settings/household#sites"`) {
			t.Errorf("no sites, member:\n%.6000s", body)
		}
		live := ben.openLive("page=search-web&datastar=" + url.QueryEscape(`{"wq":"tofu","site":"","wpages":1}`))
		if first := live.next("no sites", 5*time.Second, `id="search-web"`); strings.Contains(first, "web-hit--skeleton") {
			t.Errorf("no sites on the stream:\n%.3000s", first)
		}
		if ss.searches() != before {
			t.Errorf("searched with every site off: %d", ss.searches()-before)
		}
	})
}
