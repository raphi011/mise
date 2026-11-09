package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/mail"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

var resetLinkRe = regexp.MustCompile(`http://[^\s"]+(/reset/[A-Za-z0-9_-]+)`)

// waitMail waits for n background emails.
func waitMail(t *testing.T, wait func(context.Context, int) bool, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if !wait(ctx, n) {
		t.Fatalf("waited for %d emails", n)
	}
}

// TestPasswordResetByEmail is P4's reset criterion: with SMTP, a member
// who forgot their password gets a link by email, sets a new password,
// is signed in, and their other sessions end.
func TestPasswordResetByEmail(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv, rec := startMailServer(t, db)
		anna, _ := household(t, srv)
		phone := newBrowser(t, srv.URL)

		page := phone.get("/forgot-password").expect(t, "forgot page", http.StatusOK)
		if !strings.Contains(page.body, `name="email"`) || strings.Contains(page.body, "set up to send emails") {
			t.Fatalf("forgot page without a form:\n%.3000s", page.body)
		}
		phone.post("/forgot-password", url.Values{"email": {"ANNA@example.com"}}).
			expect(t, "request reset", http.StatusSeeOther, "/forgot-password?sent=1")
		if p := phone.get("/forgot-password?sent=1"); !strings.Contains(p.body, "Check your inbox") {
			t.Error("no check-your-inbox notice")
		}
		waitMail(t, rec.Wait, 1)
		msg := rec.Sent()[0]
		if !strings.Contains(msg.To, "anna@example.com") || msg.Subject != "Reset your mise password" ||
			!strings.Contains(msg.Text, "anna") || !strings.Contains(msg.HTML, "/reset/") {
			t.Errorf("reset email %+v", msg)
		}
		m := resetLinkRe.FindStringSubmatch(msg.Text)
		if m == nil {
			t.Fatalf("no reset link in %q", msg.Text)
		}
		link := m[1]

		// An unknown address answers the same and sends nothing.
		phone.post("/forgot-password", url.Values{"email": {"nobody@example.com"}}).
			expect(t, "unknown address", http.StatusSeeOther, "/forgot-password?sent=1")
		s.Wait()
		if n := len(rec.Sent()); n != 1 {
			t.Errorf("%d emails after an unknown address", n)
		}
		phone.post("/forgot-password", url.Values{"email": {"not an address"}}).expect(t, "bad address", http.StatusUnprocessableEntity)

		page = phone.get(link).expect(t, "reset page", http.StatusOK)
		if !strings.Contains(page.body, "For your account anna") {
			t.Errorf("reset page:\n%.3000s", page.body)
		}
		phone.post(link, url.Values{"password": {"short"}}).expect(t, "short password", http.StatusUnprocessableEntity)
		phone.post(link, url.Values{"password": {"a brand new secret"}}).expect(t, "reset", http.StatusSeeOther, "/")
		phone.get("/groceries").expect(t, "signed in after reset", http.StatusOK)
		anna.get("/groceries").expect(t, "old session after reset", http.StatusFound)
		phone.get(link).expect(t, "used link", http.StatusGone)
		newBrowser(t, srv.URL).post("/signin", url.Values{"username": {"anna"}, "password": {"a brand new secret"}}).
			expect(t, "sign in with new password", http.StatusSeeOther, "/")
	})
}

func TestPasswordResetRateLimit(t *testing.T) {
	_, srv, _ := startMailServer(t, storetest.SQLite(t))
	household(t, srv)
	b := newBrowser(t, srv.URL)
	for range resetAddrBurst {
		b.post("/forgot-password", url.Values{"email": {"anna@example.com"}}).expect(t, "request", http.StatusSeeOther)
	}
	b.post("/forgot-password", url.Values{"email": {"Anna@Example.com"}}).expect(t, "over the limit", http.StatusTooManyRequests)
}

// TestResetSubmitRateLimit: setting a password from a reset link hashes
// it, so submits are limited per client IP, valid token or not.
func TestResetSubmitRateLimit(t *testing.T) {
	_, srv, rec := startMailServer(t, storetest.SQLite(t))
	household(t, srv)
	b := newBrowser(t, srv.URL)
	b.post("/forgot-password", url.Values{"email": {"anna@example.com"}}).expect(t, "request", http.StatusSeeOther)
	waitMail(t, rec.Wait, 1)
	m := resetLinkRe.FindStringSubmatch(rec.Sent()[0].Text)
	if m == nil {
		t.Fatal("no reset link")
	}
	for range resetSubmitBurst {
		b.post("/reset/unknown-token", url.Values{"password": {"a brand new secret"}}).expect(t, "unknown token", http.StatusGone)
	}
	page := b.post(m[1], url.Values{"password": {"a brand new secret"}}).expect(t, "over the limit", http.StatusTooManyRequests)
	if !strings.Contains(page.body, `role="alert"`) {
		t.Errorf("no alert on the limited reset form:\n%.3000s", page.body)
	}
	b.get(m[1]).expect(t, "link still valid", http.StatusOK)
}

func TestPasswordResetWithoutSMTP(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	household(t, srv)
	b := newBrowser(t, srv.URL)
	if p := b.get("/forgot-password"); !strings.Contains(p.body, "set up to send emails") || strings.Contains(p.body, `name="email"`) {
		t.Errorf("forgot page without SMTP:\n%.3000s", p.body)
	}
	b.post("/forgot-password", url.Values{"email": {"anna@example.com"}}).expect(t, "request without SMTP", http.StatusNotFound)
}

// TestInviteEmail checks that an invite with an address is emailed when
// SMTP is configured, and that a failed send keeps the link.
func TestInviteEmail(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv, rec := startMailServer(t, db)
		anna, _ := household(t, srv)
		if p := anna.get("/settings/invite"); !strings.Contains(p.body, "We’ll email them the link too.") {
			t.Error("invite form does not say it emails the link")
		}
		anna.post("/settings/invites", url.Values{"role": {"member"}, "email": {"carla@example.com"}}).
			expect(t, "invite", http.StatusSeeOther, "/settings/invite?mail=sent")
		page := anna.get("/settings/invite?mail=sent")
		link := inviteLinkRe.FindStringSubmatch(page.body)
		if link == nil || !strings.Contains(page.body, "We emailed the link too.") {
			t.Fatalf("invite page after sending:\n%.3000s", page.body)
		}
		sent := rec.Sent()
		if len(sent) != 1 || sent[0].To != "<carla@example.com>" || !strings.Contains(sent[0].Text, link[1]) ||
			sent[0].Subject != "Anna invited you to Home on mise" {
			t.Fatalf("invite email %+v", sent)
		}

		// Without an address nothing is sent.
		anna.post("/settings/invites", url.Values{"role": {"member"}}).expect(t, "invite without email", http.StatusSeeOther, "/settings/invite")
		anna.get("/settings/invite")

		rec.Err = errors.New("smtp down")
		anna.post("/settings/invites", url.Values{"role": {"member"}, "email": {"dora@example.com"}}).
			expect(t, "invite, send fails", http.StatusSeeOther, "/settings/invite?mail=failed")
		page = anna.get("/settings/invite?mail=failed")
		if inviteLinkRe.FindStringSubmatch(page.body) == nil || !strings.Contains(page.body, "couldn’t be sent") {
			t.Errorf("invite page after a failed send:\n%.3000s", page.body)
		}
		if len(rec.Sent()) != 1 {
			t.Errorf("emails: %+v", rec.Sent())
		}
	})
}

// TestResetEmailLanguage checks that a reset email is in the account's
// language.
func TestResetEmailLanguage(t *testing.T) {
	msg, err := resetEmail(t.Context(), auth.ResetTicket{Username: "ben", Name: "Ben", Email: "ben@example.com", Language: "de"}, "https://mise.example.com/reset/x")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "Dein mise-Passwort zurücksetzen" || !strings.Contains(msg.Text, "Hallo Ben,") ||
		!strings.Contains(msg.HTML, `lang="de"`) || msg.To != `"Ben" <ben@example.com>` {
		t.Errorf("German reset email %+v", msg)
	}
}

// TestResetEmailsHaveOwnDeadlines: with several accounts on one address,
// a slow first send must not eat into the time the next one gets.
func TestResetEmailsHaveOwnDeadlines(t *testing.T) {
	_, srv, rec := startMailServer(t, storetest.SQLite(t))
	_, ben := household(t, srv)
	ben.post("/settings/account/profile", url.Values{"name": {"Ben"}, "email": {"anna@example.com"}, "avatar_color": {"sky"}}).
		expect(t, "share anna's address", http.StatusSeeOther)

	const slow = 300 * time.Millisecond
	var (
		mu        sync.Mutex
		deadlines []time.Time
	)
	rec.Before = func(ctx context.Context, _ mail.Message) {
		d, ok := ctx.Deadline()
		if !ok {
			t.Error("send without a deadline")
		}
		mu.Lock()
		deadlines = append(deadlines, d)
		first := len(deadlines) == 1
		mu.Unlock()
		if first {
			time.Sleep(slow)
		}
	}
	newBrowser(t, srv.URL).post("/forgot-password", url.Values{"email": {"anna@example.com"}}).
		expect(t, "request reset", http.StatusSeeOther)
	waitMail(t, rec.Wait, 2)

	mu.Lock()
	defer mu.Unlock()
	if len(deadlines) != 2 {
		t.Fatalf("%d sends, want 2", len(deadlines))
	}
	if gap := deadlines[1].Sub(deadlines[0]); gap < slow {
		t.Errorf("second send's deadline only %v after the first's: they share a budget", gap)
	}
	if left := time.Until(deadlines[1]); left < mail.Timeout-time.Second {
		t.Errorf("second send gets %v, want about %v", left, mail.Timeout)
	}
}
