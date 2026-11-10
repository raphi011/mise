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

	"github.com/raphi011/mise/internal/auth"
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

// annaSignedIn returns a browser signed in to base as anna with her password.
func annaSignedIn(t *testing.T, base string) *browser {
	t.Helper()
	b := newBrowser(t, base)
	b.post("/signin", url.Values{"username": {"anna"}, "password": {adminPassword}}).expect(t, "password sign-in", http.StatusSeeOther)
	return b
}

// ssoLink links b's account from the account page: start, sign in at the
// provider as c, and follow the provider back to the callback.
func ssoLink(t *testing.T, b *browser, idp *ssotest.Provider, c ssotest.Claims) page {
	t.Helper()
	start := b.post("/settings/account/sso/link", nil).expect(t, "start link", http.StatusSeeOther)
	return b.get(idp.Authorize(start.location, c))
}

func TestOIDCSignIn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		anna := ssotest.Claims{Subject: "sub-anna", Email: "anna@example.com", EmailVerified: true}

		// The button sits next to the password form, which stays.
		p := newBrowser(t, base).get("/signin").expect(t, "sign-in page", http.StatusOK)
		for _, want := range []string{`action="/auth/oidc/start"`, "Sign in with Pocket ID", `action="/signin"`, `name="password"`} {
			if !strings.Contains(p.body, want) {
				t.Errorf("sign-in page lacks %q", want)
			}
		}

		// Not linked yet: the provider's verified address matching Anna's
		// does not sign her in.
		refused := map[string]struct {
			claims ssotest.Claims
			want   string
		}{
			"unlinked":  {anna, "No mise account is linked to this Pocket ID account. Sign in with your password, then link it in your account settings."},
			"bad nonce": {ssotest.Claims{Subject: "sub-anna", Nonce: "replayed"}, "Signing in with Pocket ID didn't work"},
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

		// Linked from the account page, SSO signs Anna in and keeps next.
		ssoLink(t, annaSignedIn(t, base), idp, anna).expect(t, "link", http.StatusSeeOther, "/settings/account?done=linked")
		b := newBrowser(t, base)
		ssoSignIn(t, b, idp, "/plan?week=2026-11-09", ssotest.Claims{Subject: "sub-anna"}).
			expect(t, "linked sign-in", http.StatusSeeOther, "/plan?week=2026-11-09")
		p = b.get("/settings/account").expect(t, "account", http.StatusOK)
		if !strings.Contains(p.body, "Sign in with Pocket ID") || !strings.Contains(p.body, "Linked") {
			t.Errorf("account page does not show the link:\n%s", p.body)
		}

		// next never leaves the site.
		for _, next := range []string{"//evil.example/x", "https://evil.example/", "/\\evil.example"} {
			ssoSignIn(t, newBrowser(t, base), idp, next, ssotest.Claims{Subject: "sub-anna"}).
				expect(t, "next "+next, http.StatusSeeOther, "/")
		}
	})
}

// TestOIDCNoLinkByEmail: a member who sets their own email address to a
// housemate's provider address does not get the housemate's first SSO
// sign-in (security review F2).
func TestOIDCNoLinkByEmail(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		ben := addMember(t, db, "ben", "carla@example.com")
		b := newBrowser(t, base)
		p := ssoSignIn(t, b, idp, "", ssotest.Claims{Subject: "sub-carla", Email: "carla@example.com", EmailVerified: true})
		if p.status != http.StatusForbidden || !strings.Contains(p.body, "No mise account is linked") {
			t.Errorf("unlinked sign-in: status %d\n%.400s", p.status, p.body)
		}
		if b.get("/settings").status != http.StatusFound {
			t.Error("signed in as the account with the matching address")
		}
		var n int
		if err := db.System().Read().QueryRow(t.Context(), `SELECT count(*) FROM user_identities WHERE user_id = ?`, ben).Scan(&n); err != nil || n != 0 {
			t.Errorf("identities linked to ben: %d, %v", n, err)
		}
	})
}

// TestOIDCLink: linking runs the provider flow from a signed-in session
// and links the identity to that session's account only.
func TestOIDCLink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		anna := ssotest.Claims{Subject: "sub-anna"}

		b := annaSignedIn(t, base)
		p := b.get("/settings/account").expect(t, "account", http.StatusOK)
		if !strings.Contains(p.body, `action="/settings/account/sso/link"`) || !strings.Contains(p.body, "Not linked") {
			t.Errorf("account page offers no link:\n%s", p.body)
		}
		if p := newBrowser(t, base).post("/settings/account/sso/link", nil); !strings.HasPrefix(p.location, "/signin") {
			t.Errorf("link while signed out: status %d, location %q", p.status, p.location)
		}

		// The session that started the link must finish it: here it signed
		// in again in between, which is a new session.
		start := b.post("/settings/account/sso/link", nil).expect(t, "start link", http.StatusSeeOther)
		b.post("/signin", url.Values{"username": {"anna"}, "password": {adminPassword}}).expect(t, "sign in again", http.StatusSeeOther)
		p = b.get(idp.Authorize(start.location, anna))
		if p.status != http.StatusForbidden || !strings.Contains(p.body, "while linking Pocket ID") {
			t.Errorf("session changed: status %d\n%.400s", p.status, p.body)
		}
		// Signed out in between: refused on the sign-in page.
		start = b.post("/settings/account/sso/link", nil).expect(t, "start link", http.StatusSeeOther)
		b.post("/signout", nil).expect(t, "sign out", http.StatusSeeOther)
		p = b.get(idp.Authorize(start.location, anna))
		if p.status != http.StatusForbidden || !strings.Contains(p.body, `name="password"`) {
			t.Errorf("signed out: status %d\n%.400s", p.status, p.body)
		}
		ssoSignIn(t, newBrowser(t, base), idp, "", anna).expect(t, "not linked by refused attempts", http.StatusForbidden)

		b = annaSignedIn(t, base)
		ssoLink(t, b, idp, anna).expect(t, "link", http.StatusSeeOther, "/settings/account?done=linked")
		p = b.get("/settings/account?done=linked").expect(t, "account", http.StatusOK)
		if !strings.Contains(p.body, "Linked. You can sign in with it from now on.") || !strings.Contains(p.body, `action="/settings/account/sso/unlink"`) {
			t.Errorf("account page after linking:\n%s", p.body)
		}
		// Another identity of the same provider is refused.
		p = ssoLink(t, b, idp, ssotest.Claims{Subject: "sub-other"})
		if p.status != http.StatusConflict || !strings.Contains(p.body, "already linked to another Pocket ID account") {
			t.Errorf("second identity: status %d\n%.400s", p.status, p.body)
		}

		// Anna's identity cannot be linked to another account.
		benID := addMember(t, db, "ben", "ben@example.com")
		setPassword(t, db, benID, memberPassword)
		ben := newBrowser(t, base)
		ben.post("/signin", url.Values{"username": {"ben"}, "password": {memberPassword}}).expect(t, "ben signs in", http.StatusSeeOther)
		p = ssoLink(t, ben, idp, anna)
		if p.status != http.StatusConflict || !strings.Contains(p.body, "already linked to another mise account") {
			t.Errorf("identity linked elsewhere: status %d\n%.400s", p.status, p.body)
		}
		ssoSignIn(t, newBrowser(t, base), idp, "/settings/account", anna).expect(t, "still anna", http.StatusSeeOther, "/settings/account")
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
		b = annaSignedIn(t, base)
		ssoLink(t, b, idp, ssotest.Claims{Subject: "sub-anna"}).expect(t, "link after recovery", http.StatusSeeOther, "/settings/account?done=linked")
		ssoSignIn(t, newBrowser(t, base), idp, "/plan", ssotest.Claims{Subject: "sub-anna"}).
			expect(t, "sign-in after recovery", http.StatusSeeOther, "/plan")
	})
}

func TestOIDCUnlink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		idp := ssotest.New(t)
		base := startSSOServer(t, db, idp)
		b := annaSignedIn(t, base)
		ssoLink(t, b, idp, ssotest.Claims{Subject: "sub-anna"}).expect(t, "link", http.StatusSeeOther, "/settings/account?done=linked")

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

// setPassword gives the user password.
func setPassword(t *testing.T, db *store.DB, userID uuid.UUID, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	err = db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
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

// formAction returns the form-action directive of p's content security
// policy.
func formAction(t *testing.T, p page) string {
	t.Helper()
	for d := range strings.SplitSeq(p.header.Get("Content-Security-Policy"), ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "form-action ") {
			return d
		}
	}
	t.Fatalf("no form-action in %q", p.header.Get("Content-Security-Policy"))
	return ""
}

// TestSSOFormAction: the SSO button posts a form answered with a redirect
// to the provider, which the browser checks against form-action, so the
// policy lists the authorization endpoint's origin from discovery.
func TestSSOFormAction(t *testing.T) {
	t.Run("issuer origin", func(t *testing.T) {
		idp := ssotest.New(t)
		srv := startSSOServer(t, storetest.SQLite(t), idp)
		p := newBrowser(t, srv).get("/signin").expect(t, "sign-in page", http.StatusOK)
		if got, want := formAction(t, p), "form-action 'self' "+idp.URL; got != want {
			t.Errorf("%s, want %s", got, want)
		}
	})

	// A provider whose sign-in page is on another host than the issuer.
	const authOrigin = "https://login.example.net"
	movedIdP := func(t *testing.T) *ssotest.Provider {
		idp := ssotest.New(t)
		idp.SetAuthorizationEndpoint(authOrigin + "/authorize")
		return idp
	}
	t.Run("discovered at the first sign-in", func(t *testing.T) {
		b := newBrowser(t, startSSOServer(t, storetest.SQLite(t), movedIdP(t)))
		if target := ssoStart(t, b, ""); !strings.HasPrefix(target, authOrigin+"/authorize?") {
			t.Fatalf("start redirected to %q", target)
		}
		p := b.get("/signin").expect(t, "sign-in page", http.StatusOK)
		if got, want := formAction(t, p), "form-action 'self' "+authOrigin; got != want {
			t.Errorf("%s, want %s", got, want)
		}
	})
	t.Run("discovered at startup", func(t *testing.T) {
		idp := movedIdP(t)
		db := storetest.SQLite(t)
		startSSOServer(t, db, idp)
		// A restarted server: setup stored the base URL, so it discovers the
		// provider while loading its settings.
		cfg := config.Config{OIDC: config.OIDC{Issuer: idp.URL, ClientID: ssotest.ClientID, ClientSecret: ssotest.ClientSecret, Name: "Pocket ID"}}
		s := newServer(t, cfg, db, slog.New(slog.DiscardHandler))
		srv := httptest.NewServer(s.Handler())
		t.Cleanup(func() { srv.Close(); s.Wait() })
		if err := s.LoadSettings(t.Context()); err != nil {
			t.Fatal(err)
		}
		p := newBrowser(t, srv.URL).get("/signin").expect(t, "sign-in page", http.StatusOK)
		if got, want := formAction(t, p), "form-action 'self' "+authOrigin; got != want {
			t.Errorf("%s, want %s", got, want)
		}
	})
}
