package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

func newTestServer(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	return newServer(t, cfg, storetest.SQLite(t), slog.New(slog.DiscardHandler)).Handler()
}

func newServer(t *testing.T, cfg config.Config, db *store.DB, log *slog.Logger) *Server {
	t.Helper()
	if cfg.DataDir == "" {
		// Uploaded photos go here, not into the package directory.
		cfg.DataDir = t.TempDir()
	}
	s, err := New(cfg, db, log)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// response is the part of an HTTP response the tests look at.
type response struct {
	StatusCode int
	Header     http.Header
}

func do(t *testing.T, h http.Handler, req *http.Request) (response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return response{StatusCode: rec.Code, Header: rec.Header()}, rec.Body.String()
}

func TestHealth(t *testing.T) {
	h := newTestServer(t, config.Config{})
	for _, path := range []string{"/healthz", "/readyz"} {
		res, body := do(t, h, newReq(t, "GET", path, nil))
		if res.StatusCode != 200 || body != "ok\n" {
			t.Errorf("%s: %d %q", path, res.StatusCode, body)
		}
	}
}

// signedIn returns a request for target carrying the session cookie.
func signedIn(t *testing.T, c *http.Cookie, method, target string) *http.Request {
	t.Helper()
	r := newReq(t, method, target, nil)
	r.AddCookie(c)
	return r
}

func TestPages(t *testing.T) {
	h := newTestServer(t, config.Config{})
	c := doSetup(t, h)

	res, _ := do(t, h, signedIn(t, c, "GET", "/"))
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/groceries" {
		t.Errorf("/: %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}

	for path, title := range map[string]string{
		"/recipes":   "Recipes",
		"/plan":      "This week",
		"/groceries": "Groceries",
		"/settings":  "Settings",
	} {
		res, body := do(t, h, signedIn(t, c, "GET", path))
		if res.StatusCode != 200 {
			t.Errorf("%s: status %d", path, res.StatusCode)
			continue
		}
		if !strings.Contains(body, "<title>"+title+" · mise</title>") {
			t.Errorf("%s: missing title %q", path, title)
		}
		if !strings.Contains(body, `href="`+path+`" aria-current="page"`) {
			t.Errorf("%s: nav entry not marked current", path)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing security headers", path)
		}

		// Signed out, pages lead to sign-in and the shell is not shown.
		res, _ = do(t, h, newReq(t, "GET", path, nil))
		if res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), "/signin?next=") {
			t.Errorf("%s signed out: %d -> %q", path, res.StatusCode, res.Header.Get("Location"))
		}
	}

	_, body := do(t, h, newReq(t, "GET", "/signin", nil))
	if strings.Contains(body, `href="/settings"`) {
		t.Error("settings link shown to signed-out visitors")
	}
}

func TestLocale(t *testing.T) {
	h := newTestServer(t, config.Config{})
	req := newReq(t, "GET", "/setup", nil)
	req.Header.Set("Accept-Language", "de-AT,de;q=0.9")
	_, body := do(t, h, req)
	if !strings.Contains(body, `<html lang="de"`) || !strings.Contains(body, "<title>mise einrichten · mise</title>") {
		t.Errorf("German page not rendered:\n%.400s", body)
	}

	// Signed in, the household default (English, from the setup request)
	// beats Accept-Language.
	c := doSetup(t, h)
	req = signedIn(t, c, "GET", "/groceries")
	req.Header.Set("Accept-Language", "de-AT,de;q=0.9")
	if _, body := do(t, h, req); !strings.Contains(body, "<title>Groceries · mise</title>") {
		t.Errorf("household language not preferred:\n%.400s", body)
	}
}

func TestNotFound(t *testing.T) {
	h := newTestServer(t, config.Config{})
	res, _ := do(t, h, newReq(t, "GET", "/nope", nil))
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/setup" {
		t.Errorf("before setup: status %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	doSetup(t, h)
	res, body := do(t, h, newReq(t, "GET", "/nope", nil))
	if res.StatusCode != 404 || !strings.Contains(body, "Page not found") {
		t.Errorf("status %d", res.StatusCode)
	}
}

func TestStaticAssets(t *testing.T) {
	h := newTestServer(t, config.Config{})
	_, body := do(t, h, newReq(t, "GET", "/setup", nil))

	hrefs := regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(hrefs) < 3 {
		t.Fatalf("expected css, js and font assets, got %v", hrefs)
	}
	for _, m := range hrefs {
		res, _ := do(t, h, newReq(t, "GET", m[1], nil))
		if res.StatusCode != 200 {
			t.Errorf("%s: status %d", m[1], res.StatusCode)
		}
		if !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: Cache-Control %q", m[1], res.Header.Get("Cache-Control"))
		}
	}

	res, _ := do(t, h, newReq(t, "GET", "/static/css/app.css", nil))
	if res.StatusCode != 404 {
		t.Errorf("unhashed asset served: %d", res.StatusCode)
	}
}

func TestCrossOriginWritesRejected(t *testing.T) {
	base, _ := url.Parse("https://mise.example.com")
	h := newTestServer(t, config.Config{BaseURL: base})

	cross := newReq(t, "POST", "/anything", nil)
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	if res, _ := do(t, h, cross); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: status %d, want 403", res.StatusCode)
	}

	// Past CSRF, a write before setup is sent to the setup page.
	same := newReq(t, "POST", "/anything", nil)
	same.Header.Set("Sec-Fetch-Site", "same-origin")
	if res, _ := do(t, h, same); res.StatusCode != http.StatusSeeOther {
		t.Errorf("same-origin POST: status %d, want 303 to setup", res.StatusCode)
	}

	// A proxy rewrote Host, but the Origin is the configured base URL.
	proxied := newReq(t, "POST", "/anything", nil)
	proxied.Host = "mise:8080"
	proxied.Header.Set("Origin", "https://mise.example.com")
	if res, _ := do(t, h, proxied); res.StatusCode != http.StatusSeeOther {
		t.Errorf("trusted origin POST: status %d, want 303", res.StatusCode)
	}
}

func TestWriteRouteRejectsSafeMethods(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("write(%s) did not panic", m)
				}
			}()
			newRouter().write(m, "/x", http.NotFoundHandler())
		}()
	}
	newRouter().write("POST", "/x", http.NotFoundHandler())
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		name, remote, xff, want string
		trusted                 []netip.Prefix
	}{
		{"direct", "203.0.113.7:5000", "", "203.0.113.7", trusted},
		{"untrusted peer ignores xff", "203.0.113.7:5000", "1.2.3.4", "203.0.113.7", trusted},
		{"trusted proxy", "10.0.0.2:5000", "198.51.100.9", "198.51.100.9", trusted},
		{"spoofed left entries ignored", "10.0.0.2:5000", "6.6.6.6, 198.51.100.9", "198.51.100.9", trusted},
		{"chain of proxies", "10.0.0.2:5000", "198.51.100.9, 10.0.0.3", "198.51.100.9", trusted},
		{"malformed hop", "10.0.0.2:5000", "garbage", "10.0.0.2", trusted},
		{"no trusted proxies", "10.0.0.2:5000", "198.51.100.9", "10.0.0.2", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReq(t, "GET", "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientIP(r, c.trusted).String(); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func newReq(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), method, target, body)
}

func TestRequestLogIncludesRoute(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := newServer(t, config.Config{}, storetest.SQLite(t), log).Handler()
	doSetup(t, h)
	do(t, h, newReq(t, "GET", "/signin", nil))
	do(t, h, newReq(t, "GET", "/nope", nil))
	out := buf.String()
	if !strings.Contains(out, `route="GET /signin"`) {
		t.Errorf("matched route not logged:\n%s", out)
	}
	if !strings.Contains(out, `route=/ status=404`) || !strings.Contains(out, "path=/nope") {
		t.Errorf("fallback route or path not logged:\n%s", out)
	}
}

func TestRequestLogRouteForRejectedWrite(t *testing.T) {
	var buf strings.Builder
	h := newServer(t, config.Config{}, storetest.SQLite(t), slog.New(slog.NewTextHandler(&buf, nil))).Handler()
	r := newReq(t, "POST", "/invite/secret-invite-token", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if res, _ := do(t, h, r); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST: status %d, want 403", res.StatusCode)
	}
	out := buf.String()
	if !strings.Contains(out, `route="POST /invite/{token}"`) {
		t.Errorf("route of rejected write not logged:\n%s", out)
	}
	if strings.Contains(out, "secret-invite-token") {
		t.Errorf("invite token logged:\n%s", out)
	}
}

func TestReadyzRequiresMigrations(t *testing.T) {
	db, err := store.Open(t.Context(), "sqlite://"+t.TempDir()+"/mise.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := newServer(t, config.Config{}, db, slog.New(slog.DiscardHandler)).Handler()
	if res, _ := do(t, h, newReq(t, "GET", "/readyz", nil)); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unmigrated: status %d, want 503", res.StatusCode)
	}
	if _, err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if res, _ := do(t, h, newReq(t, "GET", "/readyz", nil)); res.StatusCode != http.StatusOK {
		t.Errorf("migrated: status %d, want 200", res.StatusCode)
	}
}

func TestReadyzDuringWrite(t *testing.T) {
	db := storetest.SQLite(t)
	h := newServer(t, config.Config{}, db, slog.New(slog.DiscardHandler)).Handler()
	// Hold SQLite's single writer connection while probing.
	err := db.System().Tx(t.Context(), func(context.Context, store.Querier) error {
		if res, body := do(t, h, newReq(t, "GET", "/readyz", nil)); res.StatusCode != http.StatusOK {
			t.Errorf("readyz during write: %d %q", res.StatusCode, body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAddTrustedOrigin(t *testing.T) {
	s := newServer(t, config.Config{}, storetest.SQLite(t), slog.New(slog.DiscardHandler))
	h := s.Handler()
	req := func() *http.Request {
		r := newReq(t, "POST", "/anything", nil)
		r.Host = "mise:8080"
		r.Header.Set("Origin", "https://mise.example.com")
		return r
	}
	if res, _ := do(t, h, req()); res.StatusCode != http.StatusForbidden {
		t.Errorf("before: status %d, want 403", res.StatusCode)
	}
	if err := s.AddTrustedOrigin("https://mise.example.com"); err != nil {
		t.Fatal(err)
	}
	if res, _ := do(t, h, req()); res.StatusCode != http.StatusSeeOther {
		t.Errorf("after: status %d, want 303 to setup", res.StatusCode)
	}
	if err := s.AddTrustedOrigin("not an origin"); err == nil {
		t.Error("invalid origin accepted")
	}
}

func TestFavicon(t *testing.T) {
	res, _ := do(t, newTestServer(t, config.Config{}), newReq(t, "GET", "/favicon.ico", nil))
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), "/static/img/icon.") {
		t.Errorf("status %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestRecoverPanicsLogsStack(t *testing.T) {
	var buf strings.Builder
	h := recoverPanics(slog.New(slog.NewTextHandler(&buf, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	res, _ := do(t, h, newReq(t, "GET", "/", nil))
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status %d", res.StatusCode)
	}
	if out := buf.String(); !strings.Contains(out, "panic=kaboom") || !strings.Contains(out, "stack=") || !strings.Contains(out, "TestRecoverPanicsLogsStack") {
		t.Errorf("stack not logged:\n%s", out)
	}
}

func TestRequestLogImplicitStatus(t *testing.T) {
	var buf strings.Builder
	h := logRequests(slog.New(slog.NewTextHandler(&buf, nil)), nil, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	do(t, h, newReq(t, "GET", "/", nil))
	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("implicit status not logged as 200:\n%s", buf.String())
	}
}
