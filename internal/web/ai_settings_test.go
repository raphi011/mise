package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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
	if !strings.Contains(body, `placeholder="••••`) {
		t.Error("stored key not masked in the key field")
	}
	if !strings.Contains(anna.get("/settings").body, ">Not tested<") {
		t.Error("settings overview doesn't show the AI connection status")
	}

	// Bad endpoint for an OpenAI-compatible server.
	body = anna.post("/settings/ai", url.Values{"provider": {"openai"}, "endpoint": {"ftp://x"}, "model": {"m"}}).
		expect(t, "bad endpoint", http.StatusUnprocessableEntity).body
	if !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, "ftp://x") {
		t.Errorf("endpoint error not shown:\n%.2000s", body)
	}
}

func TestAISettingsTestConnection(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	anna, ben := household(t, srv)

	// An OpenAI-compatible server that answers once, then rejects the key.
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop",
				"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(model.Close)

	form := url.Values{"enabled": {"on"}, "provider": {"openai"}, "endpoint": {model.URL + "/v1"}, "model": {"m"}, "api_key": {"sk-1"}}
	ben.post("/settings/ai/test", form).expect(t, "member tests", http.StatusForbidden)

	anna.post("/settings/ai", form).expect(t, "save", http.StatusSeeOther)
	if body := anna.get("/settings/ai").body; !strings.Contains(body, "Not tested") {
		t.Errorf("status before a test:\n%.3000s", body)
	}
	anna.post("/settings/ai/test", form).expect(t, "test", http.StatusSeeOther, "/settings/ai?done=tested")
	if body := anna.get("/settings/ai?done=tested").body; !strings.Contains(body, "conn-status--ok") || !strings.Contains(body, "The model answered") {
		t.Errorf("after a passing test:\n%.3000s", body)
	}

	// The test saves the form first: a changed model is stored and tested.
	form.Set("model", "m2")
	body := anna.post("/settings/ai/test", form).expect(t, "failing test", http.StatusOK).body
	if !strings.Contains(body, "conn-status--failed") || !strings.Contains(body, "Incorrect API key provided") || !strings.Contains(body, `value="m2"`) {
		t.Errorf("after a failing test:\n%.4000s", body)
	}

	// An invalid form is not tested.
	form.Set("endpoint", "ftp://x")
	anna.post("/settings/ai/test", form).expect(t, "invalid test", http.StatusUnprocessableEntity)
	if n := calls.Load(); n != 2 {
		t.Errorf("model called %d times, want 2", n)
	}
}
