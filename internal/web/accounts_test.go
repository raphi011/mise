package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var (
	inviteLinkRe = regexp.MustCompile(`value="(http://[^"]+/invite/[A-Za-z0-9_-]+)"`)
	memberHrefRe = regexp.MustCompile(`href="/settings/people/([0-9a-f-]{36})"`)
)

// TestAccountsEndToEnd walks P1a's done criteria over real HTTP with two
// browsers: setup, invite, accept, sign out and in, role checks, removal.
func TestAccountsEndToEnd(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		admin := newBrowser(t, srv.URL)
		member := newBrowser(t, srv.URL)

		// First run: everything leads to setup.
		admin.get("/").expect(t, "first visit", http.StatusFound, "/setup")
		admin.get("/signin").expect(t, "sign-in before setup", http.StatusFound, "/setup")
		p := admin.get("/setup").expect(t, "setup page", http.StatusOK)
		if !strings.Contains(p.body, `value="`+srv.URL+`"`) {
			t.Error("base URL not prefilled from the request origin")
		}

		bad := setupForm(srv.URL)
		bad.Set("password", "short")
		p = admin.post("/setup", bad).expect(t, "invalid setup", http.StatusUnprocessableEntity)
		if !strings.Contains(p.body, `aria-invalid="true"`) || !strings.Contains(p.body, "Use at least 10 characters.") {
			t.Error("password error not shown on the field")
		}

		admin.post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther, "/")
		p = admin.get("/groceries").expect(t, "admin groceries", http.StatusOK)
		if !strings.Contains(p.body, `class="avatar avatar--amber avatar--link"`) {
			t.Error("shell avatar does not show the admin's colour")
		}

		// Setup is locked now, for everyone.
		admin.get("/setup").expect(t, "setup after setup", http.StatusFound, "/signin")
		other := setupForm(srv.URL)
		other.Set("username", "mallory")
		member.post("/setup", other).expect(t, "second setup", http.StatusSeeOther, "/signin")
		member.post("/signin", url.Values{"username": {"mallory"}, "password": {adminPassword}}).
			expect(t, "sign-in as second-setup admin", http.StatusUnauthorized)

		// Admin invites a member.
		admin.get("/settings/invite").expect(t, "invite page", http.StatusOK)
		admin.post("/settings/invites", url.Values{"role": {"member"}, "email": {"ben@example.com"}}).
			expect(t, "create invite", http.StatusSeeOther, "/settings/invite")
		p = admin.get("/settings/invite").expect(t, "invite created", http.StatusOK)
		m := inviteLinkRe.FindStringSubmatch(p.body)
		if m == nil {
			t.Fatalf("no invite link on page:\n%s", p.body)
		}
		link := m[1]
		if !strings.HasPrefix(link, srv.URL+"/invite/") {
			t.Errorf("invite link %q does not start with the base URL", link)
		}
		if strings.Contains(admin.get("/settings/invite").body, link) {
			t.Error("invite link shown more than once")
		}
		if p := admin.get("/settings"); !strings.Contains(p.body, "ben@example.com") {
			t.Error("pending invite not listed")
		}

		// The second browser accepts it.
		p = member.get(link).expect(t, "accept page", http.StatusOK)
		if !strings.Contains(p.body, "Anna invited you") || !strings.Contains(p.body, "Home") {
			t.Error("accept page does not name inviter and household")
		}
		if !strings.Contains(p.body, `value="ben@example.com"`) {
			t.Error("invite email not prefilled")
		}
		member.post(link, url.Values{"name": {"Ben"}, "username": {"anna"}, "password": {memberPassword}}).
			expect(t, "accept with taken username", http.StatusUnprocessableEntity)
		member.post(link, url.Values{"name": {"Ben"}, "username": {"Ben"}, "email": {"ben@example.com"}, "password": {memberPassword}}).
			expect(t, "accept", http.StatusSeeOther, "/")
		member.get("/groceries").expect(t, "member groceries", http.StatusOK)
		member.get(link).expect(t, "used invite", http.StatusNotFound)

		// Both sign out and back in.
		for _, c := range []struct {
			b                  *browser
			username, password string
		}{{admin, "Anna", adminPassword}, {member, "ben", memberPassword}} {
			c.b.post("/signout", nil).expect(t, c.username+" sign out", http.StatusSeeOther, "/signin")
			c.b.get("/groceries").expect(t, c.username+" signed out", http.StatusFound, "/signin?next=%2Fgroceries")
			c.b.post("/signin", url.Values{"username": {c.username}, "password": {"wrong password"}}).
				expect(t, c.username+" wrong password", http.StatusUnauthorized)
			c.b.post("/signin", url.Values{"username": {c.username}, "password": {c.password}, "next": {"/groceries"}}).
				expect(t, c.username+" sign in", http.StatusSeeOther, "/groceries")
			c.b.get("/groceries").expect(t, c.username+" signed in", http.StatusOK)
		}

		// Members cannot use admin routes.
		member.get("/settings").expect(t, "member settings", http.StatusOK)
		member.get("/settings/invite").expect(t, "member invite page", http.StatusForbidden)
		member.post("/settings/invites", url.Values{"role": {"admin"}}).expect(t, "member creates invite", http.StatusForbidden)

		// The admin finds the member and removes them.
		var benID string
		for _, m := range memberHrefRe.FindAllStringSubmatch(admin.get("/settings").body, -1) {
			if strings.Contains(admin.get("/settings/people/"+m[1]).body, "<title>Ben · mise</title>") {
				benID = m[1]
			}
		}
		if benID == "" {
			t.Fatal("member not listed for the admin")
		}
		member.get("/settings/people/"+benID).expect(t, "member opens member page", http.StatusForbidden)
		member.post("/settings/people/"+benID+"/role", url.Values{"role": {"admin"}}).expect(t, "member promotes self", http.StatusForbidden)

		admin.post("/settings/people/"+benID+"/remove", url.Values{"confirm": {"yes"}}).
			expect(t, "remove member", http.StatusSeeOther, "/settings?done=removed#people")
		if strings.Contains(admin.get("/settings").body, ">Ben<") {
			t.Error("removed member still listed")
		}

		// Their session stops working and they cannot sign in again.
		member.get("/groceries").expect(t, "removed member", http.StatusFound, "/signin?next=%2Fgroceries")
		member.post("/signin", url.Values{"username": {"ben"}, "password": {memberPassword}}).
			expect(t, "removed member signs in", http.StatusUnauthorized)
		admin.get("/groceries").expect(t, "admin still signed in", http.StatusOK)
	})
}

func TestLastAdminOverHTTP(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	admin := newBrowser(t, srv.URL)
	admin.post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther)
	id := memberHrefRe.FindStringSubmatch(admin.get("/settings").body)
	if id == nil {
		t.Fatal("admin not listed")
	}
	p := admin.post("/settings/people/"+id[1]+"/role", url.Values{"role": {"member"}}).expect(t, "demote only admin", http.StatusConflict)
	if !strings.Contains(p.body, "at least one admin") {
		t.Error("last-admin message not shown")
	}
	admin.post("/settings/people/"+id[1]+"/remove", url.Values{"confirm": {"yes"}}).expect(t, "remove self", http.StatusConflict)
	admin.get("/settings/invite").expect(t, "still admin", http.StatusOK)
}

func TestSignInRateLimit(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	b := newBrowser(t, srv.URL)
	b.post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther)
	b.post("/signout", nil).expect(t, "sign out", http.StatusSeeOther)

	// The per-username bucket runs out first.
	var last page
	for range userBurst + 1 {
		last = b.post("/signin", url.Values{"username": {"anna"}, "password": {"wrong password"}})
	}
	last.expect(t, "attempt beyond the username burst", http.StatusTooManyRequests)
	if !strings.Contains(last.body, "Too many attempts") {
		t.Error("429 page has no friendly message")
	}
	// Even the right password is refused while limited.
	b.post("/signin", url.Values{"username": {"anna"}, "password": {adminPassword}}).expect(t, "limited sign-in", http.StatusTooManyRequests)

	// The per-IP bucket catches attempts spread over usernames.
	for i := range ipBurst {
		last = b.post("/signin", url.Values{"username": {"user" + string(rune('a'+i))}, "password": {"x"}})
	}
	last.expect(t, "attempt beyond the IP burst", http.StatusTooManyRequests)
}

func TestAccountPages(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	b := newBrowser(t, srv.URL)
	b.post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther)
	other := newBrowser(t, srv.URL)
	other.post("/signin", url.Values{"username": {"anna"}, "password": {adminPassword}}).expect(t, "second device", http.StatusSeeOther)

	b.post("/settings/preferences", url.Values{"theme": {"dark"}, "language": {"de"}}).expect(t, "preferences", http.StatusSeeOther)
	p := b.get("/settings").expect(t, "settings", http.StatusOK)
	if !strings.Contains(p.body, `<html lang="de" data-theme="dark"`) {
		t.Errorf("theme or language not applied:\n%.300s", p.body)
	}
	b.post("/settings/preferences", url.Values{"theme": {"system"}, "language": {""}}).expect(t, "reset preferences", http.StatusSeeOther)

	b.post("/settings/account/profile", url.Values{"name": {"Anna B"}, "email": {"not an email"}, "avatar_color": {"sky"}}).
		expect(t, "invalid profile", http.StatusUnprocessableEntity)
	b.post("/settings/account/profile", url.Values{"name": {"Anna B"}, "email": {""}, "avatar_color": {"sky"}}).
		expect(t, "profile", http.StatusSeeOther, "/settings/account?done=saved")
	p = b.get("/settings/account?done=saved").expect(t, "account", http.StatusOK)
	if !strings.Contains(p.body, "Saved.") || !strings.Contains(p.body, "avatar--sky") {
		t.Error("profile change not shown")
	}
	if strings.Count(p.body, "/revoke\"") != 1 {
		t.Error("other device not listed with a revoke button")
	}

	b.post("/settings/account/password", url.Values{"current_password": {"wrong"}, "new_password": {"brand new password"}}).
		expect(t, "wrong current password", http.StatusUnprocessableEntity)
	b.post("/settings/account/password", url.Values{"current_password": {adminPassword}, "new_password": {"brand new password"}}).
		expect(t, "change password", http.StatusSeeOther)
	other.get("/groceries").expect(t, "other device after password change", http.StatusFound)
	b.get("/groceries").expect(t, "this device after password change", http.StatusOK)
}

func TestForgotPasswordPage(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	b := newBrowser(t, srv.URL)
	b.post("/setup", setupForm(srv.URL)).expect(t, "setup", http.StatusSeeOther)
	b.post("/signout", nil).expect(t, "sign out", http.StatusSeeOther)
	p := b.get("/forgot-password").expect(t, "forgot password", http.StatusOK)
	if !strings.Contains(p.body, "Ask an admin") {
		t.Error("forgot-password page does not point to an admin")
	}
}

func TestStoredBaseURLLoaded(t *testing.T) {
	db := storetest.SQLite(t)
	_, srv := startServer(t, db)
	newBrowser(t, srv.URL).post("/setup", setupForm("https://mise.example.com")).expect(t, "setup", http.StatusSeeOther)

	// A restarted server trusts the stored base URL as an origin.
	s, srv2 := startServer(t, db)
	if err := s.LoadSettings(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.BaseURL() == nil || s.BaseURL().String() != "https://mise.example.com" {
		t.Fatalf("base URL = %v", s.BaseURL())
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv2.URL+"/signin", strings.NewReader("username=anna&password=x"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://mise.example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode == http.StatusForbidden {
		t.Error("stored base URL not trusted as an origin")
	}
}
