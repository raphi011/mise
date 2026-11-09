package web

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/config"
)

// TestInstallable checks what browsers need to offer installing mise: the
// manifest and service worker at the root, reachable before setup and
// without a session, with icons that load.
func TestInstallable(t *testing.T) {
	h := newTestServer(t, config.Config{})

	res, body := do(t, h, newReq(t, "GET", "/manifest.webmanifest", nil))
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var m struct {
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct {
			Src, Sizes, Type, Purpose string
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("manifest: %v\n%s", err, body)
	}
	if m.StartURL != "/" || m.Display != "standalone" {
		t.Errorf("manifest start_url %q, display %q", m.StartURL, m.Display)
	}
	sizes := map[string]bool{}
	for _, icon := range m.Icons {
		res, _ := do(t, h, newReq(t, "GET", icon.Src, nil))
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != icon.Type {
			t.Errorf("icon %s: %d %q, want %q", icon.Src, res.StatusCode, res.Header.Get("Content-Type"), icon.Type)
		}
		sizes[icon.Sizes+" "+icon.Purpose] = true
	}
	for _, want := range []string{"192x192 ", "512x512 ", "512x512 maskable"} {
		if !sizes[want] {
			t.Errorf("manifest has no %q icon: %v", want, m.Icons)
		}
	}

	res, body = do(t, h, newReq(t, "GET", "/sw.js", nil))
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/javascript") {
		t.Errorf("service worker: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if strings.Contains(body, `"fetch"`) {
		t.Error("service worker handles fetches; it should only make the app installable")
	}
	if res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("service worker Cache-Control %q", res.Header.Get("Cache-Control"))
	}

	// Every page links the manifest and the touch icon.
	_, body = do(t, h, newReq(t, "GET", "/setup", nil))
	if !strings.Contains(body, `<link rel="manifest" href="/manifest.webmanifest">`) {
		t.Error("page does not link the manifest")
	}
	touch := regexp.MustCompile(`<link rel="apple-touch-icon" href="(/static/img/[^"]+)">`).FindStringSubmatch(body)
	if touch == nil {
		t.Fatal("page has no apple-touch-icon")
	}
	if res, _ := do(t, h, newReq(t, "GET", touch[1], nil)); res.StatusCode != http.StatusOK {
		t.Errorf("apple-touch-icon: %d", res.StatusCode)
	}
}
