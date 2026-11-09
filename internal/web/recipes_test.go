package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var (
	recipeHrefRe = regexp.MustCompile(`^/recipes/([0-9a-f-]{36})$`)
	toggleRe     = regexp.MustCompile(`/recipes/[0-9a-f-]{36}/ingredients/([0-9a-f-]{36})/toggle`)
	photoRe      = regexp.MustCompile(`/images/([0-9a-f]{64})/thumb`)
)

// stickyTofu is an editor submission without JavaScript.
func stickyTofu() url.Values {
	return url.Values{
		"name":       {"Sticky tofu"},
		"servings":   {"2"},
		"prep_min":   {"10"},
		"cook_min":   {"25"},
		"new_tags":   {"Vegan, Asian"},
		"ing_key":    {"r0", "r1"},
		"ing_amount": {"400 g", "3 tbsp"},
		"ing_name":   {"Firm tofu", "Soy sauce"},
		"ing_opt":    {},
		"step":       {"Press the tofu for 10 minutes.", "Whisk the soy sauce and pour it over."},
		"step_timer": {"10", ""},
		"notes":      {"Double the glaze for 4."},
	}
}

// createRecipe posts the editor and returns the new recipe's ID.
func createRecipe(t *testing.T, b *browser, form url.Values) string {
	t.Helper()
	res := b.post("/recipes", form).expect(t, "create recipe", http.StatusSeeOther)
	m := recipeHrefRe.FindStringSubmatch(res.location)
	if m == nil {
		t.Fatalf("create redirected to %q", res.location)
	}
	return m[1]
}

// toggleFor returns the toggle URL of the ingredient named name on a
// recipe page.
func toggleFor(t *testing.T, body, name string) string {
	t.Helper()
	for _, li := range strings.Split(body, `<li class="ingredient">`)[1:] {
		if strings.Contains(li, `class="ingredient__name">`+name) {
			if m := toggleRe.FindString(li); m != "" {
				return m
			}
		}
	}
	t.Fatalf("no toggle for %s:\n%.3000s", name, body)
	return ""
}

// openLive opens a page's live stream like Datastar's @get.
func (b *browser) openLive(query string) *stream {
	b.t.Helper()
	return b.openStreamURL("/live?" + query)
}

// TestRecipeToList is P2's done-criterion: a recipe's ingredients reach the
// list one by one, live for the other member, and the recipe page follows
// the list.
func TestRecipeToList(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startTimedServer(t, db)
		anna, ben := household(t, srv)
		list := firstList(t, anna)
		id := createRecipe(t, anna, stickyTofu())

		page := anna.get("/recipes/"+id).expect(t, "recipe", http.StatusOK)
		for _, want := range []string{"Sticky tofu", "0 of 2 on Shopping list", "Firm tofu", "400 g", `class="mention"`, "Double the glaze", "Vegan"} {
			if !strings.Contains(page.body, want) {
				t.Errorf("recipe page has no %q", want)
			}
		}
		soy := toggleFor(t, page.body, "Soy sauce")

		benList := ben.openStream(list)
		benList.next("ben list initial", liveDeadline, `id="groceries-live"`)
		annaRecipe := anna.openLive("page=recipe&id=" + id + "&datastar=" + url.QueryEscape(`{"servings":4}`))
		annaRecipe.next("anna recipe initial", liveDeadline, `id="recipe-live"`, "4 servings")

		// Anna puts soy sauce on the list for 4; Ben sees it scaled and
		// tagged with the recipe.
		res := anna.datastar(http.MethodPost, soy, url.Values{"on": {"true"}, "servings": {"4"}}).expect(t, "toggle on", http.StatusOK)
		if !strings.Contains(res.body, "Soy sauce added to Shopping list") || !strings.Contains(res.body, "1 of 2 on Shopping list") {
			t.Errorf("toggle response:\n%s", res.body)
		}
		benList.next("ben sees soy sauce", liveDeadline, "Soy sauce", "6 tbsp", `class="entry__recipe">Sticky tofu`)
		annaRecipe.next("anna's page follows", liveDeadline, "1 of 2 on Shopping list")

		// Ben takes it off without JavaScript; Anna's page follows.
		ben.post(soy, url.Values{"on": {"false"}, "servings": {"2"}}).expect(t, "toggle off", http.StatusSeeOther, "/recipes/"+id+"#ingredients")
		annaRecipe.next("anna sees it off", liveDeadline, "0 of 2 on Shopping list")

		// Taking off offers Undo, which puts the entry back.
		anna.datastar(http.MethodPost, soy, url.Values{"on": {"true"}, "servings": {"2"}}).expect(t, "toggle on again", http.StatusOK)
		res = anna.datastar(http.MethodPost, soy, url.Values{"on": {"false"}, "servings": {"2"}}).expect(t, "toggle off with undo", http.StatusOK)
		if !strings.Contains(res.body, "Soy sauce taken off Shopping list") || !strings.Contains(res.body, "/ingredients/restore") {
			t.Fatalf("no Undo after taking off:\n%s", res.body)
		}
		entries := regexp.MustCompile(`name="entries" value="([^"]+)"`).FindStringSubmatch(res.body)
		if entries == nil {
			t.Fatalf("Undo carries no entries:\n%s", res.body)
		}
		anna.datastar(http.MethodPost, "/recipes/"+id+"/ingredients/restore", url.Values{"entries": {entries[1]}, "servings": {"2"}}).
			expect(t, "undo", http.StatusOK)
		benList.next("ben sees it back", liveDeadline, "Soy sauce")

		// An edit reaches the open page.
		form := stickyTofu()
		form.Set("name", "Sticky glazed tofu")
		anna.post("/recipes/"+id+"/edit", form).expect(t, "edit", http.StatusSeeOther, "/recipes/"+id)
		annaRecipe.next("anna sees the edit", liveDeadline, "Sticky glazed tofu")
		benList.next("ben's list names the renamed recipe", liveDeadline, `class="entry__recipe">Sticky glazed tofu`)

		// So does a tag renamed in Settings.
		ids := tagIDs(t, ben) // Asian, Vegan
		ben.post("/settings/tags/"+ids[1]+"/rename", url.Values{"name": {"Plant-based"}}).expect(t, "rename tag", http.StatusSeeOther)
		annaRecipe.next("anna sees the renamed tag", liveDeadline, "Plant-based")
	})
}

func TestRecipePagesWithoutJavaScript(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)

	// Validation errors come back per field, with what was typed.
	bad := stickyTofu()
	bad.Set("name", "")
	bad.Set("servings", "many")
	bad["ing_name"] = []string{"Firm tofu", strings.Repeat("x", 100)}
	res := anna.post("/recipes", bad).expect(t, "invalid recipe", http.StatusUnprocessableEntity)
	for _, want := range []string{"Fill this in.", "Enter a whole number.", "Firm tofu", "Double the glaze"} {
		if !strings.Contains(res.body, want) {
			t.Errorf("invalid recipe page has no %q", want)
		}
	}
	bad.Set("name", "Tofu")
	bad.Set("servings", "2")
	res = anna.post("/recipes", bad).expect(t, "long ingredient", http.StatusUnprocessableEntity)
	if !strings.Contains(res.body, `id="ing-error-r1"`) {
		t.Errorf("ingredient error not on its row:\n%.3000s", res.body)
	}

	// Rows are added, moved and removed by posting the form.
	form := stickyTofu()
	form.Set("ing_new", "2 tbsp maple syrup")
	form.Set("action", "add-ingredient")
	res = anna.post("/recipes", form).expect(t, "add row", http.StatusOK)
	if !strings.Contains(res.body, `value="maple syrup"`) && !strings.Contains(res.body, `value="Maple syrup"`) {
		t.Errorf("added row missing:\n%.4000s", res.body)
	}
	form.Set("ing_new", "")
	form.Set("action", "up:r1")
	res = anna.post("/recipes", form).expect(t, "move row", http.StatusOK)
	if strings.Index(res.body, `value="Soy sauce"`) > strings.Index(res.body, `value="Firm tofu"`) {
		t.Error("row not moved up")
	}

	// Enter in the add field saves the typed line too.
	form = stickyTofu()
	form.Set("ing_new", "1 thumb ginger")
	id := createRecipe(t, anna, form)
	page := anna.get("/recipes/"+id).expect(t, "recipe", http.StatusOK)
	if !strings.Contains(page.body, "0 of 3 on") {
		t.Errorf("typed line not saved:\n%.3000s", page.body)
	}

	// Servings scale without JavaScript.
	page = anna.get("/recipes/"+id+"?servings=4").expect(t, "scaled", http.StatusOK)
	if !strings.Contains(page.body, "800 g") || !strings.Contains(page.body, `name="servings" value="4"`) {
		t.Errorf("not scaled:\n%.3000s", page.body)
	}

	// Library, filters and search.
	lib := anna.get("/recipes").expect(t, "library", http.StatusOK)
	if !strings.Contains(lib.body, "Sticky tofu") || !strings.Contains(lib.body, "35 min") {
		t.Errorf("library:\n%.3000s", lib.body)
	}
	if mine := ben.get("/recipes?fav=1").expect(t, "favourites", http.StatusOK); strings.Contains(mine.body, "Sticky tofu") {
		t.Error("favourite filter shows a recipe Ben did not mark")
	}
	ben.post("/recipes/"+id+"/favourite", url.Values{"on": {"true"}}).expect(t, "favourite", http.StatusSeeOther)
	if fav := ben.get("/recipes?fav=1").expect(t, "favourites", http.StatusOK); !strings.Contains(fav.body, "Sticky tofu") {
		t.Error("favourite not shown under Favourites")
	}
	// The filter page has a generic submit; the sheet counts, live.
	if fp := anna.get("/recipes/filters").expect(t, "filter page", http.StatusOK).body; !strings.Contains(fp, ">Show recipes<") {
		t.Errorf("filter page submit:\n%.3000s", fp)
	}
	if sheet := anna.datastar(http.MethodGet, "/recipes/filters", nil).body; !strings.Contains(sheet, "Show 1 recipe") ||
		!strings.Contains(sheet, "/recipes/filters/count") {
		t.Errorf("filter sheet submit:\n%.3000s", sheet)
	}
	if n := anna.datastar(http.MethodGet, "/recipes/filters/count?fav=1&sort=name", nil).body; !strings.Contains(n, "Show 0 recipes") {
		t.Errorf("filter count:\n%s", n)
	}
	search := anna.get("/recipes/search?q=soy").expect(t, "search", http.StatusOK)
	if !strings.Contains(search.body, "Ingredient matches") || !strings.Contains(search.body, "<mark>Soy</mark>") {
		t.Errorf("search:\n%.3000s", search.body)
	}
	if none := anna.get("/recipes/search?q=bibimbap").body; !strings.Contains(none, "No “bibimbap” yet") || !strings.Contains(none, `href="/recipes/new"`) {
		t.Errorf("empty search:\n%.3000s", none)
	}
	// The highlight sits inside the word, with no space after it.
	if mid := anna.get("/recipes/search?q=sauc").body; !strings.Contains(mid, "sauc</mark>e") {
		t.Errorf("highlight splits the word:\n%.3000s", mid)
	}
	// The menu and the delete confirm are different dialogs, so the second
	// replaces the first instead of morphing the open one.
	menu := anna.datastar(http.MethodGet, "/recipes/"+id+"/menu", nil).body
	del := anna.datastar(http.MethodGet, "/recipes/"+id+"/delete", nil).body
	if !strings.Contains(menu, `<dialog id="recipe-menu"`) || !strings.Contains(del, `<dialog id="recipe-delete"`) {
		t.Errorf("sheet dialogs need their own ids:\n%.1000s\n%.1000s", menu, del)
	}
	// Markup in a search is text.
	if xss := anna.get("/recipes/search?q=" + url.QueryEscape("<b>soy")).body; strings.Contains(xss, "<b>soy") {
		t.Error("search echoes markup")
	}

	// Delete asks first, names who else loses it, and can be undone.
	confirm := anna.get("/recipes/"+id+"/delete").expect(t, "delete page", http.StatusOK)
	if !strings.Contains(confirm.body, "Ben will lose it too.") {
		t.Errorf("delete confirmation:\n%.3000s", confirm.body)
	}
	anna.get("/recipes/"+id).expect(t, "still there after GET", http.StatusOK)
	anna.post("/recipes/"+id+"/delete", nil).expect(t, "delete", http.StatusSeeOther, "/recipes?deleted="+id)
	anna.get("/recipes/"+id).expect(t, "deleted", http.StatusNotFound)
	if undo := anna.get("/recipes?deleted=" + id).body; !strings.Contains(undo, "/recipes/"+id+"/restore") {
		t.Errorf("no Undo after delete:\n%.3000s", undo)
	}
	anna.post("/recipes/"+id+"/restore", nil).expect(t, "restore", http.StatusSeeOther, "/recipes/"+id)
	anna.get("/recipes/"+id).expect(t, "restored", http.StatusOK)

	// The list row names the recipe.
	toggle := toggleFor(t, anna.get("/recipes/"+id).body, "Firm tofu")
	anna.post(toggle, url.Values{"on": {"true"}}).expect(t, "toggle", http.StatusSeeOther)
	if groc := anna.get("/groceries").body; !strings.Contains(groc, `class="entry__recipe">Sticky tofu`) {
		t.Errorf("list row has no recipe tag:\n%.3000s", groc)
	}
}

// TestEditKeepsIngredientIDs checks that saving the editor keeps the IDs
// of the rows it had, so a +/✓ tap from a recipe page opened before the
// save still works.
func TestEditKeepsIngredientIDs(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	id := createRecipe(t, anna, stickyTofu())
	soy := toggleFor(t, anna.get("/recipes/"+id).body, "Soy sauce")

	edit := anna.get("/recipes/"+id+"/edit").expect(t, "editor", http.StatusOK)
	var keys []string
	for _, m := range regexp.MustCompile(`name="ing_key" value="([^"]+)"`).FindAllStringSubmatch(edit.body, -1) {
		keys = append(keys, m[1])
	}
	if len(keys) != 2 {
		t.Fatalf("editor keys %v", keys)
	}
	form := stickyTofu()
	form["ing_key"] = []string{keys[1], keys[0], "nnew"}
	form["ing_amount"] = []string{"4 tbsp", "400 g", "1 thumb"}
	form["ing_name"] = []string{"Soy sauce", "Firm tofu", "Ginger"}
	anna.post("/recipes/"+id+"/edit", form).expect(t, "edit", http.StatusSeeOther, "/recipes/"+id)

	anna.post(soy, url.Values{"on": {"true"}}).expect(t, "stale toggle", http.StatusSeeOther, "/recipes/"+id+"#ingredients")
	if page := anna.get("/recipes/" + id).body; toggleFor(t, page, "Soy sauce") != soy || !strings.Contains(page, "1 of 3 on") {
		t.Errorf("after edit:\n%.3000s", page)
	}
}

// TestRecipeWritesNotOnGET checks that GET never changes a recipe.
func TestRecipeWritesNotOnGET(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	id := createRecipe(t, anna, stickyTofu())
	toggle := toggleFor(t, anna.get("/recipes/"+id).body, "Soy sauce")
	for _, path := range []string{
		"/recipes/" + id + "/favourite?on=true",
		toggle + "?on=true",
		"/recipes/" + id + "/ingredients/restore",
		"/recipes/" + id + "/restore",
	} {
		// No GET route: the catch-all answers Not Found.
		if res := anna.get(path); res.status != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, res.status)
		}
	}
	anna.get("/recipes/"+id+"/delete").expect(t, "confirm page", http.StatusOK)
	page := anna.get("/recipes/"+id).expect(t, "recipe", http.StatusOK)
	if !strings.Contains(page.body, "0 of 2 on") || strings.Contains(page.body, `aria-pressed="true"`) {
		t.Errorf("a GET changed the recipe:\n%.3000s", page.body)
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for x := range 40 {
		for y := range 30 {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: 120, B: uint8(y * 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// postMultipart posts the editor with a photo file, last as browsers send
// it.
func (b *browser) postMultipart(path string, form url.Values, filename string, data []byte) page {
	b.t.Helper()
	return b.postPhotos(path, form, 1, filename, data)
}

// postPhotos is postMultipart with n copies of the photo.
func (b *browser) postPhotos(path string, form url.Values, n int, filename string, data []byte) page {
	b.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range form {
		for _, v := range vs {
			if err := mw.WriteField(k, v); err != nil {
				b.t.Fatal(err)
			}
		}
	}
	for range n {
		fw, err := mw.CreateFormFile("photo_file", filename)
		if err != nil {
			b.t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			b.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		b.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodPost, b.url(path), &buf)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", b.base)
	return b.do(req)
}

func TestRecipePhoto(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)

	// HEIC is refused with its own message; the rest of the form stays.
	heic := append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 64)...)
	res := anna.postMultipart("/recipes", stickyTofu(), "IMG_0001.HEIC", heic).expect(t, "heic", http.StatusUnprocessableEntity)
	if !strings.Contains(res.body, "Convert to JPEG") || !strings.Contains(res.body, `value="Sticky tofu"`) {
		t.Errorf("heic response:\n%.3000s", res.body)
	}

	// The editor sends one photo; a post with more is refused.
	anna.postPhotos("/recipes", stickyTofu(), 2, "tofu.png", pngBytes(t)).expect(t, "two photos", http.StatusBadRequest)

	res = anna.postMultipart("/recipes", stickyTofu(), "tofu.png", pngBytes(t)).expect(t, "with photo", http.StatusSeeOther)
	id := recipeHrefRe.FindStringSubmatch(res.location)[1]
	lib := ben.get("/recipes").body
	m := photoRe.FindStringSubmatch(lib)
	if m == nil {
		t.Fatalf("no photo on the card:\n%.3000s", lib)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/images/"+m[1]+"/thumb", nil)
	if err != nil {
		t.Fatal(err)
	}
	img, err := ben.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = img.Body.Close()
	if img.StatusCode != http.StatusOK || img.Header.Get("Content-Type") != "image/jpeg" ||
		img.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Errorf("image: %d %q %q", img.StatusCode, img.Header.Get("Content-Type"), img.Header.Get("Cache-Control"))
	}
	ben.get("/images/"+strings.Repeat("a", 64)+"/thumb").expect(t, "unknown image", http.StatusNotFound)
	ben.get("/images/"+m[1]+"/huge").expect(t, "unknown size", http.StatusNotFound)
	newBrowser(t, srv.URL).get("/images/"+m[1]+"/thumb").expect(t, "signed out", http.StatusFound)

	// Editing keeps the photo unless it is removed.
	edit := stickyTofu()
	edit.Set("photo", m[1])
	anna.post("/recipes/"+id+"/edit", edit).expect(t, "edit", http.StatusSeeOther)
	if !strings.Contains(anna.get("/recipes/"+id).body, "/images/"+m[1]+"/full") {
		t.Error("photo lost on edit")
	}
	edit.Set("remove_photo", "1")
	anna.post("/recipes/"+id+"/edit", edit).expect(t, "remove photo", http.StatusSeeOther)
	if strings.Contains(anna.get("/recipes/"+id).body, "/images/") {
		t.Error("photo not removed")
	}
}

// TestNoGETWriteRoutes checks that action routes are never registered for
// GET, where cross-origin protection does not apply.
func TestNoGETWriteRoutes(t *testing.T) {
	s := newServer(t, config.Config{}, storetest.SQLite(t), slog.New(slog.DiscardHandler))
	r := newRouter()
	s.routes(r)
	verbs := []string{"/favourite", "/toggle", "/restore", "/remove", "/check", "/uncheck", "/revoke", "/move", "/restore-cart", "/signout", "/entries", "/invites", "/role", "/password", "/revoke-others", "/pin", "/merge", "/rename"}
	posts := map[string]bool{}
	for _, p := range r.patterns {
		method, path, _ := strings.Cut(p, " ")
		if method == http.MethodGet {
			for _, v := range verbs {
				if strings.HasSuffix(path, v) {
					t.Errorf("GET route for an action: %s", p)
				}
			}
		} else {
			posts[path] = true
		}
	}
	for _, path := range []string{
		"/recipes", "/recipes/{id}/edit", "/recipes/{id}/favourite", "/recipes/{id}/ingredients/{ing}/toggle",
		"/recipes/{id}/ingredients/restore", "/recipes/{id}/delete", "/recipes/{id}/restore",
	} {
		if !posts[path] {
			t.Errorf("no write route %s", path)
		}
	}
}

var tagActionRe = regexp.MustCompile(`action="/settings/tags/([0-9a-f-]{36})/rename"`)

// tagIDs returns the tag IDs on the tags page, in order.
func tagIDs(t *testing.T, b *browser) []string {
	t.Helper()
	var ids []string
	for _, m := range tagActionRe.FindAllStringSubmatch(b.get("/settings/tags").expect(t, "tags", http.StatusOK).body, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// TestTagSettings checks Settings › Recipe tags, which every member may
// edit.
func TestTagSettings(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)
	createRecipe(t, anna, stickyTofu()) // tags Asian, Vegan

	if overview := ben.get("/settings").body; !strings.Contains(overview, `href="/settings/tags"`) {
		t.Error("settings overview does not link the tags")
	}
	ben.post("/settings/tags", url.Values{"name": {"Soup"}}).expect(t, "add tag", http.StatusSeeOther, "/settings/tags?done=created")
	res := ben.post("/settings/tags", url.Values{"name": {"vegan"}}).expect(t, "duplicate", http.StatusUnprocessableEntity)
	if !strings.Contains(res.body, "There&#39;s already a tag with that name.") {
		t.Errorf("duplicate tag:\n%.2000s", res.body)
	}
	ids := tagIDs(t, ben) // Asian, Soup, Vegan
	if len(ids) != 3 {
		t.Fatalf("tags: %v", ids)
	}
	asian, soup, vegan := ids[0], ids[1], ids[2]

	ben.post("/settings/tags/"+asian+"/rename", url.Values{"name": {"Asian food"}}).expect(t, "rename", http.StatusSeeOther)
	res = ben.post("/settings/tags/"+asian+"/rename", url.Values{"name": {"Soup"}}).expect(t, "rename to taken", http.StatusUnprocessableEntity)
	if !strings.Contains(res.body, "There&#39;s already a tag with that name.") {
		t.Errorf("rename to a taken name:\n%.2000s", res.body)
	}

	// A pinned tag is a quick filter on Recipes.
	ben.post("/settings/tags/"+vegan+"/pin", url.Values{"pinned": {"true"}}).expect(t, "pin", http.StatusSeeOther)
	lib := ben.get("/recipes").body
	if !strings.Contains(lib, `href="/recipes?tag=`+vegan+`"`) || !strings.Contains(ben.get("/settings/tags").body, "Quick filter") {
		t.Errorf("pinned tag is no quick filter:\n%.3000s", lib)
	}

	// Merging moves the recipes; deleting keeps them.
	ben.post("/settings/tags/"+vegan+"/merge", url.Values{"into": {soup}}).expect(t, "merge", http.StatusSeeOther, "/settings/tags?done=merged")
	if ids := tagIDs(t, ben); len(ids) != 2 {
		t.Errorf("after merge: %v", ids)
	}
	if soupLib := ben.get("/recipes?tag=" + soup).body; !strings.Contains(soupLib, "Sticky tofu") {
		t.Error("merged recipe not under the target tag")
	}
	ben.post("/settings/tags/"+soup+"/merge", url.Values{"into": {soup}}).expect(t, "merge into itself", http.StatusBadRequest)
	ben.post("/settings/tags/"+asian+"/delete", nil).expect(t, "delete", http.StatusSeeOther, "/settings/tags?done=deleted")
	ben.post("/settings/tags/"+asian+"/delete", nil).expect(t, "delete again", http.StatusNotFound)
	if lib := ben.get("/recipes").body; !strings.Contains(lib, "Sticky tofu") {
		t.Error("recipe gone with its tag")
	}
}
