package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// bearerTransport adds a bearer token to every request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession connects an MCP client to base's /mcp with token.
func mcpSession(t *testing.T, base, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: base + mcpPath, HTTPClient: &http.Client{Transport: bearerTransport{token}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool calls a tool and decodes its structured result into out. It
// fails the test when the tool reports an error unless wantErr is set; it
// returns the error text.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var msg strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg.WriteString(tc.Text)
			}
		}
		if out != nil {
			t.Fatalf("%s failed: %s", name, msg.String())
		}
		return msg.String()
	}
	if out != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decode %s: %v", name, raw, err)
		}
	}
	return ""
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestMCPTools(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, _ := household(t, srv)

		// A read token sees only the read tools.
		reader := mcpSession(t, srv.URL, makeToken(t, anna, "reader", "read"))
		names := toolNames(t, reader)
		if !slices.Contains(names, "search_recipes") || slices.Contains(names, "create_recipe") {
			t.Errorf("read token tools: %v", names)
		}
		if _, err := reader.CallTool(t.Context(), &mcp.CallToolParams{Name: "create_recipe", Arguments: map[string]any{"name": "x", "servings": 1}}); err == nil {
			t.Error("read token called create_recipe")
		}

		cs := mcpSession(t, srv.URL, makeToken(t, anna, "writer", "write"))
		if got := cs.InitializeResult().ServerInfo.Name; got != "mise" {
			t.Errorf("server name %q", got)
		}
		var me whoamiJSON
		callTool(t, cs, "whoami", map[string]any{}, &me)
		if me.User.Username != "anna" || me.Scope != "write" || me.Today == "" || me.Version == "" {
			t.Errorf("whoami: %+v", me)
		}

		var rec recipeJSON
		callTool(t, cs, "create_recipe", map[string]any{
			"name": "Pasta aglio e olio", "servings": 2, "tags": []string{"quick"},
			"ingredients": []map[string]any{{"text": "200 g spaghetti"}, {"text": "1 pinch chili flakes", "optional": true}},
			"steps":       []map[string]any{{"text": "Boil.", "timer_seconds": 600}},
		}, &rec)
		if rec.Name != "Pasta aglio e olio" || len(rec.Ingredients) != 2 || rec.Ingredients[0].Amount != "200 g" ||
			!rec.Ingredients[1].Optional || len(rec.Steps) != 1 {
			t.Fatalf("created: %+v", rec)
		}

		// Validation errors name their fields.
		if msg := callTool(t, cs, "create_recipe", map[string]any{"name": "", "servings": 2}, nil); !strings.Contains(msg, "- name:") {
			t.Errorf("invalid recipe: %q", msg)
		}
		if msg := callTool(t, cs, "get_recipe", map[string]any{"id": "nope"}, nil); !strings.Contains(msg, "- id:") {
			t.Errorf("bad id: %q", msg)
		}
		if msg := callTool(t, cs, "get_recipe", map[string]any{"id": "0197a3c0-0000-7000-8000-000000000000"}, nil); !strings.Contains(msg, "Not found") {
			t.Errorf("unknown id: %q", msg)
		}

		var found recipeListJSON
		callTool(t, cs, "search_recipes", map[string]any{"query": "spaghetti"}, &found)
		if found.Total != 1 || found.Recipes[0].ID != rec.ID {
			t.Errorf("search: %+v", found)
		}

		// Keep the spaghetti, drop the chili, rename.
		var upd recipeJSON
		callTool(t, cs, "update_recipe", map[string]any{"id": rec.ID, "recipe": map[string]any{
			"name": "Spaghetti", "servings": 2,
			"ingredients": []map[string]any{{"id": rec.Ingredients[0].ID, "text": "250 g spaghetti"}},
		}}, &upd)
		if upd.Name != "Spaghetti" || len(upd.Ingredients) != 1 || upd.Ingredients[0].ID != rec.Ingredients[0].ID || len(upd.Tags) != 0 {
			t.Errorf("updated: %+v", upd)
		}

		var week weekJSON
		callTool(t, cs, "get_plan", map[string]any{}, &week)
		var meal mealJSON
		callTool(t, cs, "plan_meal", map[string]any{"recipe_id": rec.ID, "date": week.Today, "add_to_list": true}, &meal)
		if meal.Recipe.ID != rec.ID || meal.Date != week.Today {
			t.Errorf("meal: %+v", meal)
		}

		var list listViewJSON
		callTool(t, cs, "get_shopping_list", map[string]any{}, &list)
		if len(list.Entries) != 1 || !strings.EqualFold(list.Entries[0].Name, "spaghetti") {
			t.Fatalf("default list after planning: %+v", list)
		}
		var added entryJSON
		callTool(t, cs, "add_to_shopping_list", map[string]any{"text": "2 l milk"}, &added)
		if !strings.EqualFold(added.Name, "milk") || added.Amount != "2 l" {
			t.Errorf("added: %+v", added)
		}
		var checked entryJSON
		callTool(t, cs, "check_entry", map[string]any{"entry_id": added.ID}, &checked)
		if !checked.Checked {
			t.Errorf("check: %+v", checked)
		}
		var done mcpDone
		callTool(t, cs, "remove_entry", map[string]any{"entry_id": added.ID}, &done)
		callTool(t, cs, "remove_meal", map[string]any{"meal_id": meal.ID}, &done)
		callTool(t, cs, "delete_recipe", map[string]any{"id": rec.ID}, &done)
		if !done.OK {
			t.Error("delete not ok")
		}
		var lists listsJSON
		callTool(t, cs, "list_shopping_lists", map[string]any{}, &lists)
		if len(lists.Lists) == 0 || lists.Lists[0].ID != list.ID {
			t.Errorf("lists: %+v", lists)
		}
	})
}

func TestMCPAuth(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, _ := household(t, srv)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+mcpPath, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	challenge := res.Header.Get("WWW-Authenticate")
	if res.StatusCode != http.StatusUnauthorized ||
		!strings.Contains(challenge, `resource_metadata="`+srv.URL+wellKnownResource+mcpPath+`"`) {
		t.Errorf("no token: %d %q", res.StatusCode, challenge)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("401 without CORS headers: browser clients can't read the challenge")
	}

	// The session cookie does not open /mcp.
	req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, anna.url(mcpPath), strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if p := anna.do(req); p.status != http.StatusUnauthorized {
		t.Errorf("cookie: status %d", p.status)
	}
}
