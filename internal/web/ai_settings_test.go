package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/store/storetest"
)

func TestAISettingsPage(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)

	ben.get("/settings/ai").expect(t, "member opens AI settings", http.StatusForbidden)
	ben.post("/settings/ai", url.Values{"provider": {"anthropic"}}).expect(t, "member saves AI settings", http.StatusForbidden)
	if strings.Contains(ben.get("/settings").body, "/settings/ai") {
		t.Error("members see the AI settings link")
	}

	body := anna.get("/settings").expect(t, "settings", http.StatusOK).body
	if !strings.Contains(body, `href="/settings/ai"`) {
		t.Error("admin settings lack the AI import link")
	}
	body = anna.get("/settings/ai").expect(t, "AI settings", http.StatusOK).body
	if !strings.Contains(body, `value="claude-opus-5-5"`) || strings.Contains(body, `name="enabled" checked`) {
		t.Errorf("defaults not shown:\n%.2000s", body)
	}

	// Enabling Anthropic without a key is refused with the form kept.
	form := url.Values{"enabled": {"on"}, "provider": {"anthropic"}, "model": {"claude-opus-5-5"}, "allow_fetch_page": {"on"}}
	body = anna.post("/settings/ai", form).expect(t, "enable without key", http.StatusUnprocessableEntity).body
	if !strings.Contains(body, "Enter an API key") {
		t.Errorf("no key error:\n%.2000s", body)
	}

	form.Set("api_key", "sk-ant-secret-123")
	anna.post("/settings/ai", form).expect(t, "save", http.StatusSeeOther, "/settings/ai?done=saved")
	body = anna.get("/settings/ai?done=saved").expect(t, "saved", http.StatusOK).body
	if strings.Contains(body, "sk-ant-secret-123") {
		t.Fatal("the API key is rendered back")
	}
	if !strings.Contains(body, "A key is stored") || !strings.Contains(body, `name="allow_fetch_page" checked`) ||
		strings.Contains(body, `name="allow_search_items" checked`) {
		t.Errorf("saved settings not shown:\n%.3000s", body)
	}
	if !strings.Contains(anna.get("/settings").body, ">On<") {
		t.Error("settings overview doesn't say AI import is on")
	}

	// Bad endpoint for an OpenAI-compatible server.
	body = anna.post("/settings/ai", url.Values{"provider": {"openai"}, "endpoint": {"ftp://x"}, "model": {"m"}}).
		expect(t, "bad endpoint", http.StatusUnprocessableEntity).body
	if !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, "ftp://x") {
		t.Errorf("endpoint error not shown:\n%.2000s", body)
	}
}
