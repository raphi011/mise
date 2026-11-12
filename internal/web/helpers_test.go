package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/mail/mailtest"
	"github.com/raphi011/mise/internal/store"
)

const (
	adminPassword  = "admin password 123"
	memberPassword = "member password 456"
)

// setupForm is a valid first-run setup submission.
func setupForm(baseURL string) url.Values {
	return url.Values{
		"household":  {"Home"},
		"timezone":   {"Europe/Vienna"},
		"week_start": {"mon"},
		"base_url":   {baseURL},
		"name":       {"Anna"},
		"username":   {"anna"},
		"email":      {"anna@example.com"},
		"password":   {adminPassword},
	}
}

// postForm builds a same-origin form POST for handler-level tests.
func postForm(t *testing.T, target string, form url.Values) *http.Request {
	t.Helper()
	r := newReq(t, http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

// doSetup runs first-run setup through h and returns the admin's session
// cookie.
func doSetup(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm(t, "/setup", setupForm("http://example.com")))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("setup: status %d\n%s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatal("setup set no session cookie")
	return nil
}

// browser is an HTTP client with its own cookie jar against a test server.
// It does not follow redirects so tests can assert them, and sends the
// headers a browser sends on same-origin form posts.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newBrowser(t *testing.T, base string) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, base: base, c: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// page is a response as the tests see it.
type page struct {
	status   int
	location string
	body     string
	header   http.Header
}

func (b *browser) do(req *http.Request) page {
	b.t.Helper()
	res, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return page{status: res.StatusCode, location: res.Header.Get("Location"), body: string(body), header: res.Header}
}

func (b *browser) get(path string) page {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodGet, b.url(path), nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do(req)
}

// open gets a recipe page by any of its addresses, following the redirects
// from an ID or an old slug to the current slug.
func (b *browser) open(path string) page {
	b.t.Helper()
	p := b.get(path)
	for range 3 {
		if p.status != http.StatusFound || !strings.HasPrefix(p.location, "/recipes/") {
			break
		}
		p = b.get(p.location)
	}
	return p
}

// recipePath is the current address of the recipe with id: its slug.
func (b *browser) recipePath(id string) string {
	b.t.Helper()
	p := b.get("/recipes/" + id)
	if p.status != http.StatusFound {
		b.t.Fatalf("recipe %s: status %d, want a redirect to its slug", id, p.status)
	}
	return p.location
}

func (b *browser) post(path string, form url.Values) page {
	b.t.Helper()
	req, err := http.NewRequestWithContext(b.t.Context(), http.MethodPost, b.url(path), strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", b.base)
	return b.do(req)
}

func (b *browser) url(path string) string {
	if strings.HasPrefix(path, "http") {
		return path
	}
	return b.base + path
}

// expect fails unless p has the status (and, for redirects, location).
func (p page) expect(t *testing.T, what string, status int, location ...string) page {
	t.Helper()
	if p.status != status {
		t.Fatalf("%s: status %d, want %d (location %q)\n%.600s", what, p.status, status, p.location, p.body)
	}
	if len(location) > 0 && p.location != location[0] {
		t.Fatalf("%s: redirect to %q, want %q", what, p.location, location[0])
	}
	return p
}

// startServer serves db over HTTP for end-to-end tests.
func startServer(t *testing.T, db *store.DB) (*Server, *httptest.Server) {
	t.Helper()
	s := newServer(t, config.Config{}, db, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		srv.Close()
		// Background sends use the database; finish before it closes.
		s.Wait()
	})
	return s, srv
}

// startMailServer is startServer with a recording mailer, as if SMTP were
// configured.
func startMailServer(t *testing.T, db *store.DB) (*Server, *httptest.Server, *mailtest.Recorder) {
	t.Helper()
	s, srv := startServer(t, db)
	rec := mailtest.New()
	s.mailer = rec
	return s, srv, rec
}

// addHousehold inserts a second household with one admin (password
// adminPassword) next to the one setup created.
func addHousehold(t *testing.T, db *store.DB, household, admin string) {
	t.Helper()
	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	hid, uid, now := store.NewID(), store.NewID(), store.Now()
	err = db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		for _, st := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO households (id, name, timezone, week_start, default_language, created_at, updated_at)
				VALUES (?, ?, 'UTC', 'mon', 'en', ?, ?)`, []any{hid, household, now, now}},
			{`INSERT INTO users (id, username, username_key, name, email, password_hash, avatar_color, theme, language, created_at, updated_at)
				VALUES (?, ?, ?, ?, NULL, ?, 'sky', 'system', NULL, ?, ?)`, []any{uid, admin, auth.UsernameKey(admin), admin, hash, now, now}},
			{`INSERT INTO memberships (household_id, user_id, role, removed_at, created_at, updated_at)
				VALUES (?, ?, 'admin', NULL, ?, ?)`, []any{hid, uid, now, now}},
		} {
			if _, err := q.Exec(ctx, st.sql, st.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
