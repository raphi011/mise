package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
	"golang.org/x/text/language"
)

var (
	importHrefRe = regexp.MustCompile(`^/imports/([0-9a-f-]{36})$`)
	keepItemRe   = regexp.MustCompile(`name="item" value="([0-9a-f-]{36})"`)
	// recipePhotoRe is a recipe page's photo.
	recipePhotoRe = regexp.MustCompile(`/images/[0-9a-f]{64}/full`)
)

// startImportServer is startServer with the import runner going and a
// fetcher that may reach the loopback page server.
func startImportServer(t *testing.T, db *store.DB) (*Server, *httptest.Server) {
	t.Helper()
	s, srv := startServer(t, db)
	cfg := s.aiConfig
	cfg.Fetcher = ai.NewFetcher().AllowForTest(func(netip.AddrPort) bool { return true }, 5*time.Second)
	s.ai = ai.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.RunImports(ctx); err != nil {
			t.Errorf("run imports: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return s, srv
}

// recipeSite serves recipe pages for imports. /slow waits until release
// is closed.
type recipeSite struct {
	*httptest.Server
	release chan struct{}
	once    sync.Once
}

const tofuPage = `<!doctype html><html lang="en"><head><title>Crispy tofu</title>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Recipe","name":"Crispy tofu",
"image":"%s","recipeYield":"2","prepTime":"PT10M","cookTime":"PT20M",
"recipeIngredient":["400 g firm tofu","2 tbsp tamari or soy sauce","1 tbsp maple syrup"],
"recipeInstructions":[{"@type":"HowToStep","text":"Press the tofu."},{"@type":"HowToStep","text":"Fry it until crisp."}]}</script>
</head><body><p>Crispy tofu</p></body></html>`

func newRecipeSite(t *testing.T) *recipeSite {
	t.Helper()
	site := &recipeSite{release: make(chan struct{})}
	png := pngBytes(t)
	site.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tofu", "/slow", "/nophoto":
			if r.URL.Path == "/slow" {
				select {
				case <-site.release:
				case <-r.Context().Done():
					return
				}
			}
			img := site.URL + "/photo.png"
			if r.URL.Path == "/nophoto" {
				img = site.URL + "/missing.png"
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, tofuPage, img)
		case "/plain":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><p>Just a story about tofu.</p></body></html>`))
		case "/photo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		site.unblock()
		site.Close()
	})
	return site
}

func (s *recipeSite) unblock() { s.once.Do(func() { close(s.release) }) }

// startImport submits the import form and returns the job's ID.
func startImport(t *testing.T, b *browser, link string) string {
	t.Helper()
	res := b.post("/recipes/import", url.Values{"url": {link}}).expect(t, "start import", http.StatusSeeOther)
	m := importHrefRe.FindStringSubmatch(res.location)
	if m == nil {
		t.Fatalf("import redirected to %q", res.location)
	}
	return m[1]
}

// waitForPage gets path until its body contains want.
func waitForPage(t *testing.T, b *browser, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		body := b.get(path).body
		if strings.Contains(body, want) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never showed %q:\n%.3000s", path, want, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestImportReviewAndSave imports a page with recipe data without a
// model, adjusts the matches on the review screen and saves the recipe
// with its new item and photo.
func TestImportReviewAndSave(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)
	site := newRecipeSite(t)
	createRecipe(t, anna, stickyTofu()) // gives the household Firm tofu and Soy sauce

	form := anna.get("/recipes/import").expect(t, "form", http.StatusOK).body
	if !strings.Contains(form, "Import a recipe") || !strings.Contains(form, `name="url"`) {
		t.Errorf("import form with AI off:\n%.3000s", form)
	}
	anna.post("/recipes/import", url.Values{"url": {"ftp://x"}}).expect(t, "bad link", http.StatusUnprocessableEntity)

	job := startImport(t, anna, site.URL+"/tofu")
	page := "/imports/" + job
	body := waitForPage(t, anna, page, "Review import")
	for _, want := range []string{"Crispy tofu", "10 min prep · 20 min cook · 2 servings · 2 steps", "3 found · 1 matched · 1 to check · 1 new",
		"match--matched", "match--check", "match--catalog", "Maple syrup", "Save recipe &amp; 1 new item", page + "/photo", "Read the recipe page on 127.0.0.1"} {
		if !strings.Contains(body, want) {
			t.Errorf("review lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("review:\n%.6000s", body)
	}

	// Other members don't see someone else's import.
	ben.get(page).expect(t, "other member's import", http.StatusNotFound)
	ben.post(page+"/save", nil).expect(t, "other member saves", http.StatusNotFound)

	res := anna.get(page + "/photo")
	if res.status != http.StatusOK || !strings.HasPrefix(res.body, "\x89PNG") {
		t.Errorf("photo proxy: %d %.20q", res.status, res.body)
	}

	// Keep the checked match, and pick a different new item for the syrup.
	m := keepItemRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no Keep button")
	}
	anna.post(page+"/items", url.Values{"row": {"1"}, "item": {m[1]}}).expect(t, "keep", http.StatusSeeOther, page+"#ingredients")
	pick := anna.get(page+"/items?row=2").expect(t, "picker", http.StatusOK).body
	if !strings.Contains(pick, "1 tbsp maple syrup") || !strings.Contains(pick, `value="Maple syrup"`) {
		t.Errorf("picker:\n%.3000s", pick)
	}
	if found := anna.get(page + "/items?row=2&q=tofu").body; !strings.Contains(found, "Firm tofu") {
		t.Errorf("picker search for tofu:\n%.3000s", found)
	}
	anna.post(page+"/items", url.Values{"row": {"2"}, "name": {""}}).expect(t, "empty new item", http.StatusUnprocessableEntity)
	anna.post(page+"/items", url.Values{"row": {"2"}, "name": {"Agave syrup"}}).expect(t, "new item", http.StatusSeeOther)
	anna.get(page+"/items?row=9").expect(t, "row out of range", http.StatusNotFound)
	body = anna.get(page).body
	if !strings.Contains(body, "3 found · 2 matched · 0 to check · 1 new") || !strings.Contains(body, "Agave syrup") {
		t.Fatalf("review after picking:\n%.6000s", body)
	}

	res = anna.post(page+"/save", nil).expect(t, "save", http.StatusSeeOther)
	if recipeHrefRe.FindStringSubmatch(res.location) == nil {
		t.Fatalf("save redirected to %q", res.location)
	}
	recipe := anna.get(res.location).body
	for _, want := range []string{"Crispy tofu", "Agave syrup", "Firm tofu", "Soy sauce", "Fry it until crisp."} {
		if !strings.Contains(recipe, want) {
			t.Errorf("saved recipe lacks %q", want)
		}
	}
	if !recipePhotoRe.MatchString(recipe) {
		t.Error("saved recipe has no photo")
	}
	if strings.Contains(recipe, "couldn’t be downloaded") {
		t.Error("photo failure toast after a good photo")
	}
	anna.get(page).expect(t, "job after save", http.StatusNotFound)
	anna.post(page+"/save", nil).expect(t, "second save", http.StatusNotFound)
}

// TestImportRunningAndCancel follows a running import: the page refreshes
// itself without JavaScript, the live stream patches its steps and sends
// the page to the review when it is done, and Cancel stops another.
func TestImportRunningAndCancel(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	site := newRecipeSite(t)

	job := startImport(t, anna, site.URL+"/slow")
	page := "/imports/" + job
	body := waitForPage(t, anna, page, "Reading the recipe page on 127.0.0.1…")
	for _, want := range []string{`<noscript><meta http-equiv="refresh" content="3"></noscript>`, "Cancel import", "/live?page=import&amp;job=" + job, "<progress"} {
		if !strings.Contains(body, want) {
			t.Errorf("running page lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("running page:\n%.4000s", body)
	}
	live := anna.openLive("page=import&job=" + job)
	live.next("running region", 5*time.Second, "import-running", "Reading the recipe page")

	// A second and third import may queue; a fourth is refused.
	second := startImport(t, anna, site.URL+"/slow")
	startImport(t, anna, site.URL+"/slow")
	refused := anna.post("/recipes/import", url.Values{"url": {site.URL + "/slow"}}).expect(t, "fourth import", http.StatusUnprocessableEntity).body
	if !strings.Contains(refused, "You already have 3 imports running") {
		t.Errorf("fourth import:\n%.3000s", refused)
	}
	anna.post("/imports/"+second+"/cancel", nil).expect(t, "cancel", http.StatusSeeOther, "/recipes")
	deadline := time.Now().Add(5 * time.Second)
	for anna.get("/imports/"+second).status != http.StatusNotFound {
		if time.Now().After(deadline) {
			t.Fatal("canceled import still there")
		}
		time.Sleep(20 * time.Millisecond)
	}

	site.unblock()
	live.next("reload when done", 5*time.Second, "window.location", page)
	if body := anna.get(page).body; !strings.Contains(body, "Review import") || strings.Contains(body, "http-equiv") {
		t.Errorf("finished import:\n%.3000s", body)
	}
}

// TestImportFailedScreen shows a page without recipe data while AI import
// is off: the error, the form with the link kept, and the way out, without
// a word about AI.
func TestImportFailedScreen(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	site := newRecipeSite(t)

	job := startImport(t, anna, site.URL+"/plain")
	page := "/imports/" + job
	body := waitForPage(t, anna, page, "Couldn’t find recipe data on that page")
	for _, want := range []string{"Only pages with recipe data can be imported", `value="` + site.URL + `/plain"`, "Type it myself", "Import again", `href="/recipes/new"`} {
		if !strings.Contains(body, want) {
			t.Errorf("failed screen lacks %q", want)
		}
	}
	if strings.Contains(body, " AI") {
		t.Error("failed screen mentions AI while AI import is off")
	}
	if t.Failed() {
		t.Fatalf("failed screen:\n%.4000s", body)
	}
	if !strings.Contains(body, `name="replace" value="`+job+`"`) {
		t.Error("failed screen doesn't replace the failed import")
	}
	anna.post(page+"/save", nil).expect(t, "save a failure", http.StatusSeeOther, page)
	anna.get(page+"/items?row=0").expect(t, "picker of a failure", http.StatusFound)
	anna.post(page+"/discard", nil).expect(t, "discard", http.StatusSeeOther, "/recipes")
	anna.get(page).expect(t, "after discard", http.StatusNotFound)

	job = startImport(t, anna, site.URL+"/missing")
	page = "/imports/" + job
	waitForPage(t, anna, page, "Couldn’t open that page")

	// Import again replaces the failed import.
	res := anna.post("/recipes/import", url.Values{"url": {site.URL + "/tofu"}, "replace": {job}}).expect(t, "import again", http.StatusSeeOther)
	if res.location == page {
		t.Fatal("import again reused the failed job")
	}
	anna.get(page).expect(t, "replaced import", http.StatusNotFound)
}

// TestImportOpenInEditor opens a draft in the editor, prefilled, and saves
// it from there; the photo that can't be downloaded is left out and the
// recipe page says so.
func TestImportOpenInEditor(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	site := newRecipeSite(t)

	job := startImport(t, anna, site.URL+"/nophoto")
	page := "/imports/" + job
	waitForPage(t, anna, page, "Review import")
	editor := anna.get("/recipes/new?import="+job).expect(t, "editor", http.StatusOK).body
	for _, want := range []string{`value="Crispy tofu"`, `value="400 g"`, `value="Firm tofu"`, `value="Soy sauce"`, "Fry it until crisp.",
		`name="import" value="` + job + `"`, `name="import_photo"`, page + "/photo", `href="` + page + `"`} {
		if !strings.Contains(editor, want) {
			t.Errorf("editor lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("editor:\n%.8000s", editor)
	}
	anna.get("/recipes/new?import="+"00000000-0000-0000-0000-000000000000").expect(t, "unknown import", http.StatusNotFound)

	form := url.Values{
		"name": {"Crispy tofu"}, "servings": {"2"}, "import": {job}, "import_photo": {"1"},
		"ing_key": {"n1"}, "ing_amount": {"400 g"}, "ing_name": {"Firm tofu"},
		"step": {"Press the tofu."}, "step_timer": {""},
	}
	res := anna.post("/recipes", form).expect(t, "save from editor", http.StatusSeeOther)
	if recipeHrefRe.FindStringSubmatch(res.location) == nil {
		t.Fatalf("editor save redirected to %q", res.location)
	}
	recipe := anna.get(res.location).body
	if !strings.Contains(recipe, "The photo couldn’t be downloaded") || recipePhotoRe.MatchString(recipe) {
		t.Errorf("recipe after a failed photo:\n%.4000s", recipe)
	}
	if again := anna.get(res.location).body; strings.Contains(again, "The photo couldn’t be downloaded") {
		t.Error("photo toast shown twice")
	}
	anna.get(page).expect(t, "job after editor save", http.StatusNotFound)
}

// recipeModel is an OpenAI-compatible endpoint whose model answers every
// request with a create_recipe call for a dal, or with no tool call when
// the text holds no recipe.
func recipeModel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(string(body), "lentils") {
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop",
				"message":{"role":"assistant","content":"There is no recipe in this text."}}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`)
			return
		}
		args, _ := json.Marshal(map[string]any{
			"name": "Red lentil dal", "description": "", "prep_min": 5, "cook_min": 25, "servings": 4, "source_url": "", "image_url": "",
			"ingredients": []map[string]string{{"text": "250 g red lentils", "item": ""}, {"text": "1 onion", "item": ""}},
			"steps":       []map[string]any{{"text": "Simmer the lentils.", "timer_seconds": 1200}},
			"tags":        []string{},
		})
		res, _ := json.Marshal(map[string]any{
			"id": "c2", "object": "chat.completion", "created": 1, "model": "m",
			"choices": []map[string]any{{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []map[string]any{{"id": "call_1", "type": "function", "function": map[string]any{"name": "create_recipe", "arguments": string(args)}}},
			}}},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10},
		})
		_, _ = w.Write(res)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestImportPastedText imports pasted text with AI import on, and shows
// the Pasted text tab only then.
func TestImportPastedText(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)
	site := newRecipeSite(t)

	if body := anna.get("/recipes/import").body; strings.Contains(body, `name="text"`) || strings.Contains(body, "Pasted text") {
		t.Error("pasted text offered with AI import off")
	}
	off := anna.post("/recipes/import", url.Values{"tab": {"text"}, "text": {"250 g red lentils"}}).expect(t, "text with AI off", http.StatusUnprocessableEntity).body
	if !strings.Contains(off, "only links can be imported") {
		t.Errorf("text with AI off:\n%.3000s", off)
	}

	model := recipeModel(t)
	anna.post("/settings/ai", url.Values{"enabled": {"on"}, "provider": {"openai"}, "endpoint": {model.URL + "/v1"}, "model": {"m"},
		"api_key": {"sk-1"}, "allow_search_items": {"on"}}).expect(t, "turn AI on", http.StatusSeeOther)

	form := anna.get("/recipes/import?tab=text").body
	for _, want := range []string{"Import with AI", `name="tab" value="text" checked`, `name="text"`, "Import text", "Uses m."} {
		if !strings.Contains(form, want) {
			t.Errorf("form with AI on lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("form:\n%.4000s", form)
	}
	empty := anna.post("/recipes/import", url.Values{"tab": {"text"}, "text": {"  "}, "url": {"https://example.com"}}).
		expect(t, "empty text", http.StatusUnprocessableEntity).body
	if !strings.Contains(empty, `value="text" checked`) {
		t.Error("empty text lost the tab")
	}

	res := anna.post("/recipes/import", url.Values{"tab": {"text"}, "text": {"Dal: 250 g red lentils, 1 onion. Simmer."}, "url": {"ignored"}}).
		expect(t, "import text", http.StatusSeeOther)
	page := res.location
	body := waitForPage(t, anna, page, "Review import")
	for _, want := range []string{"Red lentil dal", "Read the pasted text", "Built the draft “Red lentil dal”", "250 g red lentils", "Save recipe &amp; 2 new items"} {
		if !strings.Contains(body, want) {
			t.Errorf("review lacks %q", want)
		}
	}
	if t.Failed() {
		t.Fatalf("review:\n%.5000s", body)
	}

	// Text without a recipe fails; the failed screen keeps it on the
	// Pasted text tab. A link that fails also offers pasted text.
	res = anna.post("/recipes/import", url.Values{"tab": {"text"}, "text": {"Just a story."}}).expect(t, "import a story", http.StatusSeeOther)
	body = waitForPage(t, anna, res.location, "Couldn’t find a recipe in that text")
	for _, want := range []string{`value="text" checked`, ">Just a story.</textarea>", "Import text", "Type it myself"} {
		if !strings.Contains(body, want) {
			t.Errorf("failed text import lacks %q", want)
		}
	}
	job := startImport(t, anna, site.URL+"/missing")
	body = waitForPage(t, anna, "/imports/"+job, "Couldn’t open that page")
	if !strings.Contains(body, `value="text" checked`) || !strings.Contains(body, `value="`+site.URL+`/missing"`) {
		t.Errorf("failed link import with AI on:\n%.4000s", body)
	}
}

// TestImportEntriesAndLibrary checks the ways into import (the editor's
// card, the library's Import button) and the viewer's imports above the
// library, which follow the jobs live.
func TestImportEntriesAndLibrary(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)
	site := newRecipeSite(t)

	editor := anna.get("/recipes/new").body
	if !strings.Contains(editor, `class="import-card" href="/recipes/import"`) || !strings.Contains(editor, "Import a recipe") {
		t.Errorf("new recipe with AI off lacks the import card:\n%.4000s", editor)
	}
	recipeID := createRecipe(t, anna, stickyTofu())
	if body := anna.open("/recipes/" + recipeID + "/edit").body; strings.Contains(body, "import-card") {
		t.Error("editing a recipe offers import")
	}
	library := anna.get("/recipes").body
	if !strings.Contains(library, `href="/recipes/import"`) || strings.Contains(library, "import-jobs") {
		t.Errorf("library before imports:\n%.4000s", library)
	}

	live := anna.openLive("page=recipes")
	live.next("library opens", 5*time.Second, "recipes-live")
	job := startImport(t, anna, site.URL+"/slow")
	live.next("import listed", 5*time.Second, "Importing…", `href="/imports/`+job+`"`, "127.0.0.1")
	if body := ben.get("/recipes").body; strings.Contains(body, "import-jobs") {
		t.Error("another member sees the import")
	}
	site.unblock()
	live.next("import ready", 5*time.Second, "Ready to review", "Crispy tofu")
	failed := startImport(t, anna, site.URL+"/missing")
	live.next("import failed", 5*time.Second, "Import failed", `href="/imports/`+failed+`"`)

	anna.post("/settings/ai", url.Values{"enabled": {"on"}, "provider": {"ollama"}, "model": {"m"}}).expect(t, "turn AI on", http.StatusSeeOther)
	if body := anna.get("/recipes/new").body; !strings.Contains(body, "Have a link? Import it with AI") {
		t.Errorf("new recipe with AI on:\n%.4000s", body)
	}
	if body := anna.get("/recipes/new?import=" + job).body; strings.Contains(body, "import-card") {
		t.Error("editor opened from an import offers import")
	}
}

// TestStepText words a step in the tense of its state.
func TestStepText(t *testing.T) {
	ctx := i18n.WithLocale(t.Context(), language.English)
	for _, c := range []struct {
		st   ai.JobStep
		want string
	}{
		{ai.JobStep{Kind: ai.StepReadPage, Text: "bbc.co.uk", State: ai.StepRunning}, "Reading the recipe page on bbc.co.uk…"},
		{ai.JobStep{Kind: ai.StepReadPage, Text: "bbc.co.uk", State: ai.StepDone}, "Read the recipe page on bbc.co.uk"},
		{ai.JobStep{Kind: ai.StepFetchPage, Text: "/print", State: ai.StepFailed}, "Couldn’t open /print"},
		{ai.JobStep{Kind: ai.StepReadText, State: ai.StepRunning}, "Reading the pasted text…"},
		{ai.JobStep{Kind: ai.StepFindRecipe, State: ai.StepRunning}, "Looking for the recipe…"},
		{ai.JobStep{Kind: ai.StepNextTurn, State: ai.StepFailed}, "Couldn’t go over the results"},
		{ai.JobStep{Kind: ai.StepMatch, Text: "3/5", State: ai.StepDone}, "Matched 3 of 5 ingredients to your items"},
		{ai.JobStep{Kind: "unknown", Text: "as is", State: ai.StepDone}, "as is"},
	} {
		if got := stepText(ctx, c.st); got != c.want {
			t.Errorf("%+v: %q, want %q", c.st, got, c.want)
		}
	}
}

// With JavaScript the import form opens as a dialog over the library; a
// failed submit refreshes only the form, since patching the open dialog
// would close it.
func TestImportDialog(t *testing.T) {
	_, srv := startImportServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)

	datastar := func(method, path string, form url.Values) page {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, anna.url(path), strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Datastar-Request", "true")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.Header.Set("Origin", anna.base)
		}
		return anna.do(req)
	}

	open := datastar(http.MethodGet, "/recipes/import", nil)
	for _, want := range []string{`id="sheet"`, `<dialog id="import-sheet"`, `id="import-form"`, `@post(`} {
		if !strings.Contains(open.body, want) {
			t.Errorf("dialog lacks %q:\n%.1500s", want, open.body)
		}
	}
	if !strings.Contains(anna.get("/recipes").body, `@get(&#39;/recipes/import&#39;)`) {
		t.Error("library's Import link does not open the dialog")
	}

	bad := datastar(http.MethodPost, "/recipes/import", url.Values{"url": {"not a url"}})
	if !strings.Contains(bad.body, `id="import-body"`) || strings.Contains(bad.body, "<dialog") || !strings.Contains(bad.body, "not a url") {
		t.Errorf("failed submit patches the dialog itself or loses the input:\n%.1500s", bad.body)
	}

	// Without JavaScript it is still a page.
	if p := anna.get("/recipes/import").expect(t, "import page", http.StatusOK); strings.Contains(p.body, "<dialog") {
		t.Error("plain page renders a dialog")
	}
}
