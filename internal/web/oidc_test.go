package web

import (
	"context"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/sso/ssotest"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// startSSOServer serves db with single sign-on through idp, set up with
// Anna (anna@example.com) as admin, and returns the server's URL.
func startSSOServer(t *testing.T, db *store.DB, idp *ssotest.Provider) string {
	t.Helper()
	cfg := config.Config{OIDC: config.OIDC{
		Issuer: idp.URL, ClientID: ssotest.ClientID, ClientSecret: ssotest.ClientSecret, Name: "Pocket ID",
	}}
	s := newServer(t, cfg, db, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Wait() })
	newBrowser(t, srv.URL).post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther)
	return srv.URL
}

// addMember adds an account with an email address to the household.
func addMember(t *testing.T, db *store.DB, username, email string) uuid.UUID {
	t.Helper()
	id := store.NewID()
	now := store.Now()
	err := db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		if _, err := q.Exec(ctx, `INSERT INTO users (id, username, username_key, name, email, password_hash, avatar_color, theme, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'unusable', 'sky', 'system', ?, ?)`, id, username, username, username, email, now, now); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role, created_at, updated_at)
			SELECT id, ?, 'member', ?, ? FROM households`, id, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ssoStart posts the sign-in page's SSO button and returns the provider URL
// it redirects to.
func ssoStart(t *testing.T, b *browser, next string) string {
	t.Helper()
	p := b.post("/auth/oidc/start", url.Values{"next": {next}}).expect(t, "start", http.StatusSeeOther)
	return p.location
}

// ssoSignIn runs the whole flow: start, sign in at the provider as c, and
// follow the provider back to the callback.
func ssoSignIn(t *testing.T, b *browser, idp *ssotest.Provider, next string, c ssotest.Claims) page {
	t.Helper()
	return b.get(idp.Authorize(ssoStart(t, b, next), c))
}

func TestOIDCSignIn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)

		// The button sits next to the password form, which stays.
		p := newBrowser(t, base).get("/signin").expect(t, "sign-in page", http.StatusOK)
		for _, want := range []string{`action="/auth/oidc/start"`, "Sign in with Pocket ID", `action="/signin"`, `name="password"`} {
			if !strings.Contains(p.body, want) {
				t.Errorf("sign-in page lacks %q", want)
			}
		}

		refused := map[string]struct {
			claims ssotest.Claims
			want   string
		}{
			"unverified": {ssotest.Claims{Subject: "sub-anna", Email: "anna@example.com"}, "hasn't verified your email"},
			"unknown":    {ssotest.Claims{Subject: "sub-x", Email: "x@example.com", EmailVerified: true}, "No mise account uses this email. Ask an admin for an invite."},
			"bad nonce":  {ssotest.Claims{Subject: "sub-anna", Email: "anna@example.com", EmailVerified: true, Nonce: "replayed"}, "Signing in with Pocket ID didn't work"},
		}
		for name, tc := range refused {
			b := newBrowser(t, base)
			p := ssoSignIn(t, b, idp, "/plan", tc.claims)
			if p.status < 400 || !strings.Contains(html.UnescapeString(p.body), tc.want) || !strings.Contains(p.body, `name="password"`) {
				t.Errorf("%s: status %d, want an error page with %q and the password form\n%.400s", name, p.status, tc.want, p.body)
			}
			if b.get("/settings").status != http.StatusFound {
				t.Errorf("%s: signed in after refusal", name)
			}
		}

		// First sign-in links Anna by her verified email and keeps next.
		b := newBrowser(t, base)
		ssoSignIn(t, b, idp, "/plan?week=2026-11-09", ssotest.Claims{Subject: "sub-anna", Email: "ANNA@example.com", EmailVerified: true}).
			expect(t, "first sign-in", http.StatusSeeOther, "/plan?week=2026-11-09")
		p = b.get("/settings/account").expect(t, "account", http.StatusOK)
		if !strings.Contains(p.body, "Sign in with Pocket ID") || !strings.Contains(p.body, "Linked") {
			t.Errorf("account page does not show the link:\n%s", p.body)
		}

		// Linked: the provider's email no longer matters.
		ssoSignIn(t, newBrowser(t, base), idp, "", ssotest.Claims{Subject: "sub-anna", Email: "new@example.com"}).
			expect(t, "linked sign-in", http.StatusSeeOther, "/")

		// next never leaves the site.
		for _, next := range []string{"//evil.example/x", "https://evil.example/", "/\\evil.example"} {
			ssoSignIn(t, newBrowser(t, base), idp, next, ssotest.Claims{Subject: "sub-anna"}).
				expect(t, "next "+next, http.StatusSeeOther, "/")
		}
	})
}

func TestOIDCAmbiguousEmail(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		addMember(t, db, "ben", "shared@example.com")
		addMember(t, db, "bea", "Shared@Example.com")
		p := ssoSignIn(t, newBrowser(t, base), idp, "", ssotest.Claims{Subject: "s", Email: "shared@example.com", EmailVerified: true})
		if p.status != http.StatusForbidden || !strings.Contains(p.body, "Several mise accounts use this email") {
			t.Errorf("ambiguous email: status %d\n%.400s", p.status, p.body)
		}
	})
}

func TestOIDCStateBoundToBrowser(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		anna := ssotest.Claims{Subject: "sub-anna", Email: "anna@example.com", EmailVerified: true}

		// A callback started in another browser (login CSRF) is refused.
		cb := idp.Authorize(ssoStart(t, newBrowser(t, base), "/"), anna)
		other := newBrowser(t, base)
		if p := other.get(cb); p.status != http.StatusBadRequest {
			t.Errorf("callback without flow cookie: status %d", p.status)
		}

		// A tampered state is refused, and the flow cannot be retried.
		b := newBrowser(t, base)
		u, _ := url.Parse(idp.Authorize(ssoStart(t, b, "/"), anna))
		q := u.Query()
		good := q.Get("state")
		q.Set("state", "forged")
		u.RawQuery = q.Encode()
		if p := b.get(u.String()); p.status != http.StatusBadRequest {
			t.Errorf("forged state: status %d", p.status)
		}
		q.Set("state", good)
		u.RawQuery = q.Encode()
		if p := b.get(u.String()); p.status != http.StatusBadRequest {
			t.Errorf("replayed callback after failure: status %d", p.status)
		}

		// The provider reporting an error is shown, not followed.
		start := ssoStart(t, b, "/")
		su, _ := url.Parse(start)
		back, _ := url.Parse(su.Query().Get("redirect_uri"))
		back.RawQuery = url.Values{"error": {"access_denied"}, "state": {su.Query().Get("state")}}.Encode()
		if p := b.get(back.String()); p.status != http.StatusUnauthorized || !strings.Contains(html.UnescapeString(p.body), "didn't work") {
			t.Errorf("provider error: status %d", p.status)
		}
	})
}

func TestOIDCProviderDown(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		idp.SetDown(true)
		base := startSSOServer(t, db, idp)

		b := newBrowser(t, base)
		p := b.post("/auth/oidc/start", url.Values{"next": {"/plan"}})
		if p.status != http.StatusServiceUnavailable || !strings.Contains(html.UnescapeString(p.body), "can't be reached") || !strings.Contains(p.body, `name="password"`) {
			t.Errorf("start while down: status %d\n%.400s", p.status, p.body)
		}
		b.post("/signin", url.Values{"username": {"anna"}, "password": {adminPassword}}).
			expect(t, "password sign-in while provider down", http.StatusSeeOther, "/")

		// Discovery is retried once the provider is back.
		idp.SetDown(false)
		ssoSignIn(t, newBrowser(t, base), idp, "/plan", ssotest.Claims{Subject: "sub-anna", Email: "anna@example.com", EmailVerified: true}).
			expect(t, "sign-in after recovery", http.StatusSeeOther, "/plan")
	})
}

func TestOIDCUnlink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		b := newBrowser(t, base)
		ssoSignIn(t, b, idp, "", ssotest.Claims{Subject: "sub-anna", Email: "anna@example.com", EmailVerified: true}).
			expect(t, "sign-in", http.StatusSeeOther, "/")

		// An account without a password keeps its link.
		setAnnaPassword(t, db, "")
		if p := b.post("/settings/account/sso/unlink", nil); p.status != http.StatusConflict {
			t.Errorf("unlink without password: status %d", p.status)
		}
		setAnnaPassword(t, db, "restored")

		b.post("/settings/account/sso/unlink", nil).expect(t, "unlink", http.StatusSeeOther, "/settings/account?done=unlinked")
		p := b.get("/settings/account?done=unlinked").expect(t, "account", http.StatusOK)
		if !strings.Contains(p.body, "Not linked") {
			t.Errorf("account page still linked:\n%s", p.body)
		}
		if p := b.post("/settings/account/sso/unlink", nil); p.status != http.StatusNotFound {
			t.Errorf("unlink twice: status %d", p.status)
		}
	})
}

func setAnnaPassword(t *testing.T, db *store.DB, hash string) {
	t.Helper()
	err := db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE users SET password_hash = ? WHERE username_key = 'anna'`, hash)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOIDCNeedsBaseURL(t *testing.T) {
	var logs strings.Builder
	cfg := config.Config{OIDC: config.OIDC{Issuer: "https://id.example.com", ClientID: "c", ClientSecret: "s", Name: "SSO"}}
	s := newServer(t, cfg, storetest.SQLite(t), slog.New(slog.NewTextHandler(&logs, nil)))
	if err := s.LoadSettings(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.ssoAvailable() {
		t.Error("SSO offered without a base URL")
	}
	if !strings.Contains(logs.String(), "OIDC") {
		t.Errorf("no warning logged: %q", logs.String())
	}
	// Without OIDC there is neither a button nor a start route.
	h := newTestServer(t, config.Config{})
	doSetup(t, h)
	if res, _ := do(t, h, postForm(t, "/auth/oidc/start", nil)); res.StatusCode != http.StatusNotFound {
		t.Errorf("start without OIDC: status %d", res.StatusCode)
	}
	if _, body := do(t, h, newReq(t, http.MethodGet, "/signin", nil)); strings.Contains(body, "/auth/oidc/start") {
		t.Error("SSO button without OIDC")
	}
}
