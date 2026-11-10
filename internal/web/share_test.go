package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var publicURLRe = regexp.MustCompile(`id="public-url"[^>]*value="(https?://[^"]+(/s/[A-Za-z0-9_-]+))"`)

// publicLink returns the path of the public link shown in a menu, or fails.
func publicLink(t *testing.T, body string) string {
	t.Helper()
	m := publicURLRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no public link:\n%.3000s", body)
	}
	return m[2]
}

// rawGet fetches path with b's cookies, sending header as name-value
// pairs, and returns the status and headers with the body, for tests that
// look at headers.
func (b *browser) rawGet(path string, header ...string) (response, string) {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, b.url(path), nil)
	if err != nil {
		b.t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return response{StatusCode: res.StatusCode, Header: res.Header}, string(body)
}

// expectPublicHeaders checks the headers every public link response carries.
func expectPublicHeaders(t *testing.T, what string, res response) {
	t.Helper()
	if !strings.Contains(res.Header.Get("X-Robots-Tag"), "noindex") ||
		res.Header.Get("Referrer-Policy") != "no-referrer" ||
		res.Header.Get("Set-Cookie") != "" {
		t.Errorf("%s: headers %v", what, res.Header)
	}
}

// TestPublicLink covers a recipe's public link: created and turned off with
// and without JavaScript, read by a visitor without an account, live on the
// recipe page, and confined to its one recipe.
func TestPublicLink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startTimedServer(t, db)
		anna, ben := household(t, srv)
		visitor := newBrowser(t, srv.URL)

		res := anna.postMultipart("/recipes", stickyTofu(), "tofu.png", pngBytes(t)).expect(t, "create", http.StatusSeeOther)
		id := recipeHrefRe.FindStringSubmatch(res.location)[1]
		photo := photoRe.FindStringSubmatch(anna.get("/recipes").body)[1]

		menu := anna.get("/recipes/"+id+"/menu").expect(t, "menu", http.StatusOK)
		if !strings.Contains(menu.body, `action="/recipes/`+id+`/share"`) || !strings.Contains(menu.body, "Create public link") {
			t.Fatalf("menu offers no public link:\n%.3000s", menu.body)
		}

		benRecipe := ben.openLive("page=recipe&id=" + id + "&datastar=" + url.QueryEscape(`{"servings":2}`))
		benRecipe.next("ben recipe initial", liveDeadline, `id="recipe-live"`)

		// Anna turns it on without JavaScript; Ben's open page shows it.
		anna.post("/recipes/"+id+"/share", nil).expect(t, "create link", http.StatusSeeOther, "/recipes/"+id+"/menu")
		benRecipe.next("ben sees the link on", liveDeadline, "Public link on")
		menu = anna.get("/recipes/"+id+"/menu").expect(t, "menu with link", http.StatusOK)
		link := publicLink(t, menu.body)
		if !strings.Contains(menu.body, "turned on by you") || !strings.Contains(menu.body, `action="/recipes/`+id+`/share/revoke"`) {
			t.Errorf("menu with link:\n%.3000s", menu.body)
		}
		if got := publicLink(t, ben.get("/recipes/"+id+"/menu").body); got != link {
			t.Errorf("ben sees %s, anna %s", got, link)
		}

		// A visitor reads the recipe, and nothing about the household.
		pres, body := visitor.rawGet(link)
		if pres.StatusCode != http.StatusOK {
			t.Fatalf("public page: %d\n%.2000s", pres.StatusCode, body)
		}
		expectPublicHeaders(t, "public page", pres)
		if pres.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("public page Cache-Control %q", pres.Header.Get("Cache-Control"))
		}
		for _, want := range []string{"<title>Sticky tofu · mise</title>", "Firm tofu", "400 g", "for 10 minutes.", `class="mention"`,
			`<meta name="robots" content="noindex, nofollow, noarchive">`, `<meta name="referrer" content="no-referrer">`,
			`src="` + link + `/photo/full"`} {
			if !strings.Contains(body, want) {
				t.Errorf("public page has no %q", want)
			}
		}
		for _, leak := range []string{"Anna", "Ben", "Double the glaze", "Vegan", "Shopping list", "/images/",
			`href="/recipes`, `href="/plan`, `href="/groceries`, `href="/settings`, `href="/signin`,
			"datastar.js", "app.js", "manifest.webmanifest", "<form", "Cook mode"} {
			if strings.Contains(body, leak) {
				t.Errorf("public page shows %q", leak)
			}
		}
		// Signed in, the link still shows the public page: no session, no
		// app shell.
		if _, body := anna.rawGet(link); strings.Contains(body, `href="/settings"`) || !strings.Contains(body, "Firm tofu") {
			t.Errorf("signed-in member's public page:\n%.2000s", body)
		}
		// Visitors read amounts in their units.
		if _, body := visitor.rawGet(link, "Accept-Language", "en-US"); !strings.Contains(body, "14 oz") {
			t.Errorf("en-US visitor does not read US units:\n%.3000s", body)
		}

		// The photo is the shared recipe's only, revalidated every time.
		img, _ := visitor.rawGet(link + "/photo/full")
		if img.StatusCode != http.StatusOK || img.Header.Get("Content-Type") != "image/jpeg" ||
			img.Header.Get("Cache-Control") != "private, no-cache" || img.Header.Get("ETag") == "" {
			t.Errorf("public photo: %d %v", img.StatusCode, img.Header)
		}
		expectPublicHeaders(t, "public photo", img)
		if again, _ := visitor.rawGet(link+"/photo/full", "If-None-Match", img.Header.Get("ETag")); again.StatusCode != http.StatusNotModified {
			t.Errorf("revalidated photo: %d", again.StatusCode)
		}
		visitor.get(link+"/photo/huge").expect(t, "unknown size", http.StatusNotFound)
		visitor.get("/images/"+photo+"/full").expect(t, "members' image route", http.StatusFound)
		visitor.get(link+"/recipes").expect(t, "path under the link", http.StatusNotFound)
		visitor.get("/recipes/"+id).expect(t, "recipe page", http.StatusFound)

		// Ben turns it off with JavaScript: the menu entry and a toast come
		// back, the page follows and the link stops working at once.
		off := ben.datastar(http.MethodPost, "/recipes/"+id+"/share/revoke", url.Values{}).expect(t, "revoke", http.StatusOK)
		if !strings.Contains(off.body, `id="public-link"`) || !strings.Contains(off.body, "Create public link") ||
			!strings.Contains(off.body, "Public link turned off") {
			t.Errorf("revoke response:\n%s", off.body)
		}
		if ev := benRecipe.next("ben sees the link off", liveDeadline, `id="recipe-live"`); strings.Contains(ev, "Public link on") {
			t.Error("recipe page still shows the link")
		}
		gone, body := visitor.rawGet(link)
		if gone.StatusCode != http.StatusNotFound || !strings.Contains(body, "This link doesn’t work") || strings.Contains(body, `href="/recipes`) {
			t.Errorf("revoked link: %d\n%.2000s", gone.StatusCode, body)
		}
		expectPublicHeaders(t, "revoked link", gone)
		visitor.get(link+"/photo/full").expect(t, "revoked photo", http.StatusNotFound)

		// Turned on again with JavaScript, it is a new link.
		on := ben.datastar(http.MethodPost, "/recipes/"+id+"/share", url.Values{}).expect(t, "create with js", http.StatusOK)
		fresh := publicLink(t, on.body)
		if fresh == link || !strings.Contains(on.body, "Public link created") {
			t.Errorf("second link %s (first %s):\n%s", fresh, link, on.body)
		}
		visitor.get(link).expect(t, "old link", http.StatusNotFound)
		visitor.get(fresh).expect(t, "new link", http.StatusOK)

		// Without JavaScript, turning off returns to the menu.
		anna.post("/recipes/"+id+"/share/revoke", nil).expect(t, "revoke without js", http.StatusSeeOther, "/recipes/"+id+"/menu")
		visitor.get(fresh).expect(t, "revoked without js", http.StatusNotFound)
		anna.post("/recipes/"+id+"/share", nil).expect(t, "create again", http.StatusSeeOther)
		fresh = publicLink(t, anna.get("/recipes/"+id+"/menu").body)

		// A recipe in the bin is not shown; restoring brings the link back.
		anna.post("/recipes/"+id+"/delete", nil).expect(t, "delete", http.StatusSeeOther)
		visitor.get(fresh).expect(t, "binned recipe", http.StatusNotFound)
		anna.post("/recipes/"+id+"/restore", nil).expect(t, "restore", http.StatusSeeOther)
		visitor.get(fresh).expect(t, "restored recipe", http.StatusOK)

		// Another household's link shows its recipe only, and members of
		// this household cannot touch it.
		other := storetest.NewHousehold(t, db, "other")
		osc := db.ScopeFor(other.ID)
		stew, err := s.recipes.Create(t.Context(), osc, other.Admin, recipes.Input{Name: "Secret stew", Servings: 2, Notes: "Grandma's"})
		if err != nil {
			t.Fatal(err)
		}
		sh, err := s.recipes.CreateShare(t.Context(), osc, other.Admin, stew)
		if err != nil {
			t.Fatal(err)
		}
		_, body = visitor.rawGet("/s/" + sh.Token)
		if !strings.Contains(body, "Secret stew") || strings.Contains(body, "Sticky tofu") || strings.Contains(body, "Grandma") {
			t.Errorf("other household's link:\n%.2000s", body)
		}
		if _, body := visitor.rawGet(fresh); strings.Contains(body, "Secret stew") {
			t.Error("this household's link shows the other's recipe")
		}
		// The other recipe has no photo; its link does not fall back to any.
		visitor.get("/s/"+sh.Token+"/photo/full").expect(t, "other link's photo", http.StatusNotFound)
		anna.post("/recipes/"+stew.String()+"/share/revoke", nil).expect(t, "revoke other's link", http.StatusNotFound)
		if p := anna.datastar(http.MethodPost, "/recipes/"+stew.String()+"/share", url.Values{}).expect(t, "share other's recipe", http.StatusOK); !strings.Contains(p.body, "no longer exists") {
			t.Errorf("sharing other's recipe:\n%s", p.body)
		}
		anna.get("/recipes/"+stew.String()+"/menu").expect(t, "other's menu", http.StatusNotFound)
		visitor.get("/s/"+sh.Token).expect(t, "other's link untouched", http.StatusOK)
		if got, ok, err := s.recipes.Share(t.Context(), osc, stew); err != nil || !ok || got.Token != sh.Token {
			t.Errorf("other's link changed: %+v %v %v", got, ok, err)
		}
	})
}
