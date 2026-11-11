package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var newTokenRe = regexp.MustCompile(`id="new-token" type="text" readonly value="(mise_[A-Za-z0-9_-]+)"`)

// makeToken creates a token through the account page and returns it.
func makeToken(t *testing.T, b *browser, name, scope string) string {
	t.Helper()
	b.post("/settings/account/tokens", url.Values{"name": {name}, "scope": {scope}, "expiry": {"never"}}).
		expect(t, "create token", http.StatusSeeOther, "/settings/account/tokens")
	m := newTokenRe.FindStringSubmatch(b.get("/settings/account/tokens").expect(t, "tokens page", http.StatusOK).body)
	if m == nil {
		t.Fatal("tokens page does not show the new token")
	}
	return m[1]
}

// apiClient calls the API with a bearer token and no cookies.
type apiClient struct {
	t     *testing.T
	base  string
	token string
}

type apiResult struct {
	status int
	header http.Header
	body   []byte
}

func (r apiResult) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("not JSON (status %d): %v\n%.400s", r.status, err, r.body)
	}
	return v
}

func (c apiClient) do(method, path string, body any) apiResult {
	c.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return apiResult{status: res.StatusCode, header: res.Header, body: raw}
}

func (c apiClient) want(r apiResult, status int) apiResult {
	c.t.Helper()
	if r.status != status {
		c.t.Fatalf("status %d, want %d\n%.600s", r.status, status, r.body)
	}
	return r
}

func errorCode(t *testing.T, r apiResult) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestAPIAuth(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, _ := household(t, srv)
		readTok := makeToken(t, anna, "reader", "read")
		writeTok := makeToken(t, anna, "writer", "write")

		anon := apiClient{t: t, base: srv.URL}
		r := anon.want(anon.do("GET", "/api/v1/me", nil), http.StatusUnauthorized)
		if errorCode(t, r) != "unauthorized" || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("no token: %s %v", r.body, r.header)
		}
		bad := apiClient{t: t, base: srv.URL, token: "mise_nope"}
		if errorCode(t, bad.want(bad.do("GET", "/api/v1/me", nil), http.StatusUnauthorized)) != "invalid_token" {
			t.Error("bad token not reported as invalid_token")
		}

		// A signed-in browser session does not open the API.
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, anna.url("/api/v1/me"), nil)
		if p := anna.do(req); p.status != http.StatusUnauthorized {
			t.Errorf("cookie session on the API: status %d", p.status)
		}
		// And a token does not open the pages.
		req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/recipes", nil)
		req.Header.Set("Authorization", "Bearer "+writeTok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.Request.URL.Path != "/signin" {
			t.Errorf("token opened a page: ended at %s", res.Request.URL.Path)
		}

		read := apiClient{t: t, base: srv.URL, token: readTok}
		me := read.want(read.do("GET", "/api/v1/me", nil), http.StatusOK).json(t)
		if me["scope"] != "read" || me["user"].(map[string]any)["username"] != "anna" {
			t.Errorf("me: %v", me)
		}
		r = read.want(read.do("POST", "/api/v1/recipes", map[string]any{"name": "x"}), http.StatusForbidden)
		if errorCode(t, r) != "insufficient_scope" {
			t.Errorf("read token wrote: %s", r.body)
		}

		// Every write route refuses a read token, before it looks at the body
		// or the resource.
		const id = "00000000-0000-0000-0000-000000000001"
		for _, rt := range []struct{ method, path string }{
			{"POST", "/api/v1/recipes"}, {"PUT", "/api/v1/recipes/" + id}, {"DELETE", "/api/v1/recipes/" + id},
			{"POST", "/api/v1/plan/meals"}, {"DELETE", "/api/v1/plan/meals/" + id},
			{"POST", "/api/v1/lists/" + id + "/entries"}, {"POST", "/api/v1/entries/" + id + "/check"},
			{"POST", "/api/v1/entries/" + id + "/uncheck"}, {"DELETE", "/api/v1/entries/" + id},
		} {
			if got := errorCode(t, read.want(read.do(rt.method, rt.path, map[string]any{}), http.StatusForbidden)); got != "insufficient_scope" {
				t.Errorf("%s %s with a read token: %s", rt.method, rt.path, got)
			}
		}

		// Unknown routes answer in JSON.
		if errorCode(t, read.want(read.do("GET", "/api/v1/nope", nil), http.StatusNotFound)) != "not_found" {
			t.Error("unknown API route is not a JSON 404")
		}
	})
}

func TestAPIRecipesPlanAndLists(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, _ := household(t, srv)
		api := apiClient{t: t, base: srv.URL, token: makeToken(t, anna, "agent", "write")}

		// Create: ingredients and steps may be plain lines.
		created := api.want(api.do("POST", "/api/v1/recipes", map[string]any{
			"name": "Pasta", "servings": 2, "tags": []string{"quick"}, "prep_min": 5, "cook_min": 10,
			"ingredients": []any{"200 g spaghetti", map[string]any{"name": "salt", "amount": "1 pinch", "optional": true}},
			"steps":       []any{"Boil water.", map[string]any{"text": "Cook the pasta.", "timer_seconds": 600}},
		}), http.StatusCreated).json(t)
		id := created["id"].(string)
		if created["name"] != "Pasta" || len(created["ingredients"].([]any)) != 2 || len(created["steps"].([]any)) != 2 || len(created["tags"].([]any)) != 1 {
			t.Fatalf("created %v", created)
		}
		first := created["ingredients"].([]any)[0].(map[string]any)
		if !strings.EqualFold(first["name"].(string), "spaghetti") || first["amount"] != "200 g" {
			t.Errorf("ingredient line not parsed: %v", first)
		}
		if got := api.want(api.do("GET", "/api/v1/recipes/"+id, nil), http.StatusOK).json(t); got["id"] != id {
			t.Errorf("get: %v", got)
		}

		// The web UI sees it too.
		if !strings.Contains(anna.get("/recipes").body, "Pasta") {
			t.Error("API recipe missing from the library")
		}

		list := api.want(api.do("GET", "/api/v1/recipes", nil), http.StatusOK).json(t)
		if list["total"].(float64) != 1 || len(list["recipes"].([]any)) != 1 {
			t.Errorf("list: %v", list)
		}
		found := api.want(api.do("GET", "/api/v1/recipes?q=spagh", nil), http.StatusOK).json(t)
		if len(found["recipes"].([]any)) != 1 {
			t.Errorf("ingredient search: %v", found)
		}
		none := api.want(api.do("GET", "/api/v1/recipes?q=zzz", nil), http.StatusOK).json(t)
		if len(none["recipes"].([]any)) != 0 {
			t.Errorf("search for nothing: %v", none)
		}

		// Validation and malformed bodies.
		r := api.want(api.do("POST", "/api/v1/recipes", map[string]any{"name": "", "servings": 2}), http.StatusUnprocessableEntity)
		fields := r.json(t)["error"].(map[string]any)["fields"].(map[string]any)
		if fields["name"] == nil {
			t.Errorf("no name error: %s", r.body)
		}
		if errorCode(t, api.want(api.do("POST", "/api/v1/recipes", `{"name":"x","bogus":1}`), http.StatusBadRequest)) != "invalid_json" {
			t.Error("unknown field accepted")
		}
		api.want(api.do("POST", "/api/v1/recipes", `{"name":"x"} {}`), http.StatusBadRequest)
		api.want(api.do("GET", "/api/v1/recipes/not-a-uuid", nil), http.StatusNotFound)

		// Update replaces ingredients, keeping ids that are sent back.
		upd := api.want(api.do("PUT", "/api/v1/recipes/"+id, map[string]any{
			"name": "Pasta aglio e olio", "servings": 4,
			"ingredients": []any{map[string]any{"id": first["id"], "name": "spaghetti", "amount": "400 g"}, "2 cloves garlic"},
			"steps":       []string{"Cook."},
		}), http.StatusOK).json(t)
		ings := upd["ingredients"].([]any)
		if upd["name"] != "Pasta aglio e olio" || len(ings) != 2 || ings[0].(map[string]any)["id"] != first["id"] || ings[0].(map[string]any)["amount"] != "400 g" {
			t.Errorf("updated %v", upd)
		}

		// Plan it, with its ingredients on the list.
		meal := api.want(api.do("POST", "/api/v1/plan/meals", map[string]any{
			"recipe_id": id, "date": "2026-11-11", "add_to_list": true,
		}), http.StatusCreated).json(t)
		if meal["date"] != "2026-11-11" || meal["servings"].(float64) != 4 {
			t.Errorf("meal %v", meal)
		}
		week := api.want(api.do("GET", "/api/v1/plan?date=2026-11-11", nil), http.StatusOK).json(t)
		var planned int
		for _, d := range week["days"].([]any) {
			planned += len(d.(map[string]any)["meals"].([]any))
		}
		if len(week["days"].([]any)) != 7 || planned != 1 {
			t.Errorf("week: %v", week)
		}
		api.want(api.do("GET", "/api/v1/plan?date=soon", nil), http.StatusBadRequest)
		api.want(api.do("POST", "/api/v1/plan/meals", map[string]any{"recipe_id": id, "date": "nope"}), http.StatusUnprocessableEntity)
		api.want(api.do("POST", "/api/v1/plan/meals", map[string]any{"recipe_id": "00000000-0000-0000-0000-000000000000", "date": "2026-11-11"}), http.StatusNotFound)

		// The list got the ingredients.
		lists := api.want(api.do("GET", "/api/v1/lists", nil), http.StatusOK).json(t)["lists"].([]any)
		if len(lists) == 0 {
			t.Fatal("no lists")
		}
		listID := lists[0].(map[string]any)["id"].(string)
		entries := api.want(api.do("GET", "/api/v1/lists/"+listID, nil), http.StatusOK).json(t)["entries"].([]any)
		if len(entries) != 2 {
			t.Fatalf("entries after planning: %v", entries)
		}

		// Add by text and by parts, then check, uncheck and remove.
		milk := api.want(api.do("POST", "/api/v1/lists/"+listID+"/entries", map[string]any{"text": "2 l milk"}), http.StatusCreated).json(t)
		if !strings.EqualFold(milk["name"].(string), "milk") || milk["amount"] != "2 l" || milk["checked"] != false {
			t.Errorf("milk %v", milk)
		}
		api.want(api.do("POST", "/api/v1/lists/"+listID+"/entries", map[string]any{"name": "eggs", "amount": "6"}), http.StatusCreated)
		api.want(api.do("POST", "/api/v1/lists/"+listID+"/entries", map[string]any{"text": "a", "name": "b"}), http.StatusUnprocessableEntity)
		milkID := milk["id"].(string)
		if c := api.want(api.do("POST", "/api/v1/entries/"+milkID+"/check", nil), http.StatusOK).json(t); c["checked"] != true {
			t.Errorf("check: %v", c)
		}
		if c := api.want(api.do("POST", "/api/v1/entries/"+milkID+"/uncheck", nil), http.StatusOK).json(t); c["checked"] != false {
			t.Errorf("uncheck: %v", c)
		}
		api.want(api.do("DELETE", "/api/v1/entries/"+milkID, nil), http.StatusNoContent)
		api.want(api.do("DELETE", "/api/v1/entries/"+milkID, nil), http.StatusNotFound)

		// Remove the meal and the recipe.
		api.want(api.do("DELETE", "/api/v1/plan/meals/"+meal["id"].(string), nil), http.StatusNoContent)
		api.want(api.do("DELETE", "/api/v1/recipes/"+id, nil), http.StatusNoContent)
		api.want(api.do("GET", "/api/v1/recipes/"+id, nil), http.StatusNotFound)
	})
}

func TestAPITokenPage(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, ben := household(t, srv)

		anna.get("/settings/account/tokens").expect(t, "empty page", http.StatusOK)
		tok := makeToken(t, anna, "Agent-7", "write")
		// Shown once: the page lists the token without its value afterwards.
		p := anna.get("/settings/account/tokens").expect(t, "second view", http.StatusOK)
		if strings.Contains(p.body, tok) || !strings.Contains(p.body, "Agent-7") || !strings.Contains(p.body, "Read and write") {
			t.Errorf("token page after creation:\n%s", p.body)
		}
		for _, want := range []string{"token-row__facts", "never used", `id="token-name"`} {
			if !strings.Contains(p.body, want) {
				t.Errorf("token page lacks %q", want)
			}
		}
		if p.header.Get("Cache-Control") != "no-store" {
			t.Error("tokens page is cacheable")
		}

		// Validation keeps the form.
		bad := anna.post("/settings/account/tokens", url.Values{"name": {""}, "scope": {"read"}, "expiry": {"never"}}).
			expect(t, "no name", http.StatusUnprocessableEntity)
		if !strings.Contains(bad.body, `aria-invalid="true"`) {
			t.Error("name error not shown")
		}
		anna.post("/settings/account/tokens", url.Values{"name": {"x"}, "scope": {"root"}, "expiry": {"never"}}).
			expect(t, "bad scope", http.StatusUnprocessableEntity)

		// Another member neither sees nor can revoke it.
		if strings.Contains(ben.get("/settings/account/tokens").body, "Agent-7") {
			t.Error("ben sees anna's token")
		}
		id := regexp.MustCompile(`/settings/account/tokens/([0-9a-f-]{36})/revoke`).FindStringSubmatch(p.body)
		if id == nil {
			t.Fatal("no revoke button")
		}
		ben.post("/settings/account/tokens/"+id[1]+"/revoke", nil).expect(t, "ben revokes", http.StatusNotFound)

		api := apiClient{t: t, base: srv.URL, token: tok}
		api.want(api.do("GET", "/api/v1/me", nil), http.StatusOK)
		anna.post("/settings/account/tokens/"+id[1]+"/revoke", nil).expect(t, "revoke", http.StatusSeeOther, "/settings/account/tokens?done=tokenRevoked")
		api.want(api.do("GET", "/api/v1/me", nil), http.StatusUnauthorized)
		if !strings.Contains(anna.get("/settings/account/tokens?done=tokenRevoked").body, "Token revoked") {
			t.Error("no revoke notice")
		}
	})
}
