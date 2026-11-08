package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

const testPassword = "correct horse battery"

// testHash is computed once; argon2id is deliberately slow.
var testHash = sync.OnceValue(func() string {
	h, err := HashPassword(testPassword)
	if err != nil {
		panic(err)
	}
	return h
})

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newService(t *testing.T, db *store.DB) (*Service, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
	return New(db, Config{Now: clock.Now}), clock
}

func setupInput() SetupInput {
	return SetupInput{
		HouseholdName: "Home", Timezone: "Europe/Vienna", WeekStart: "mon", Language: "en",
		BaseURL: "https://mise.example.com/", Name: "Anna", Username: "Anna", Email: "anna@example.com",
		Password: testPassword, UserAgent: "test",
	}
}

func TestPasswordHash(t *testing.T) {
	h := testHash()
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected encoding %q", h)
	}
	if ok, err := VerifyPassword(h, testPassword); !ok || err != nil {
		t.Errorf("correct password: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(h, testPassword+"x"); ok || err != nil {
		t.Errorf("wrong password: ok=%v err=%v", ok, err)
	}
	other, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if other == h {
		t.Error("two hashes of the same password are equal; salt missing")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AAAA$AAAA", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"} {
		if _, err := VerifyPassword(bad, testPassword); err == nil {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestPasswordRules(t *testing.T) {
	errs := FieldErrors{}
	checkNewPassword(errs, "p", "short")
	if errs["p"] != "form.error.passwordShort" {
		t.Errorf("short password: %v", errs)
	}
	errs = FieldErrors{}
	checkNewPassword(errs, "p", "ümlautsümlauts")
	if len(errs) != 0 {
		t.Errorf("10+ runes rejected: %v", errs)
	}
	errs = FieldErrors{}
	checkNewPassword(errs, "p", strings.Repeat("x", MaxPasswordLen+1))
	if errs["p"] != "form.error.passwordLong" {
		t.Errorf("long password: %v", errs)
	}
}

func TestUsernameKey(t *testing.T) {
	if UsernameKey(" Anna ") != UsernameKey("anna") {
		t.Error("case or space not folded")
	}
	if UsernameKey("STRASSE") != UsernameKey("straße") {
		t.Error("full case folding not applied")
	}
	errs := FieldErrors{}
	checkUsername(errs, "u", "an na")
	if errs["u"] != "form.error.usernameChars" {
		t.Errorf("space accepted: %v", errs)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "/",
		"/recipes":            "/recipes",
		"/plan?week=2":        "/plan?week=2",
		"//evil.com":          "/",
		"/\\evil.com":         "/",
		"https://evil.com/":   "/",
		"recipes":             "/",
		"/x\r\nSet-Cookie: a": "/",
	} {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLimiter(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := NewLimiter(3, 10*time.Second, 100, clock.Now)
	for i := range 3 {
		if !l.Allow("a") {
			t.Fatalf("attempt %d refused within burst", i+1)
		}
	}
	if l.Allow("a") {
		t.Fatal("attempt beyond burst allowed")
	}
	if !l.Allow("b") {
		t.Fatal("other key affected")
	}
	clock.Add(10 * time.Second)
	if !l.Allow("a") {
		t.Fatal("token not refilled after interval")
	}
	if l.Allow("a") {
		t.Fatal("more than one token refilled")
	}

	// Idle buckets are swept once they are full again.
	clock.Add(time.Minute)
	l.Allow("c")
	if n := l.Len(); n != 1 {
		t.Errorf("after sweep: %d keys, want 1", n)
	}
}

func TestLimiterBounded(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	l := NewLimiter(1, time.Hour, 10, clock.Now)
	for i := range 1000 {
		l.Allow(uuid.NewString())
		if l.Len() > 10 {
			t.Fatalf("after %d keys: %d tracked, max 10", i+1, l.Len())
		}
	}
}

func TestSetup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)

		if need, err := s.NeedsSetup(ctx); err != nil || !need {
			t.Fatalf("NeedsSetup = %v, %v", need, err)
		}

		bad := setupInput()
		bad.Timezone = "Mars/Olympus"
		bad.Password = "short"
		bad.BaseURL = "ftp://x"
		var fe FieldErrors
		if _, err := s.Setup(ctx, bad); !errors.As(err, &fe) || fe["timezone"] == "" || fe["password"] == "" || fe["base_url"] == "" {
			t.Fatalf("invalid input: %v", err)
		}

		res, err := s.Setup(ctx, setupInput())
		if err != nil {
			t.Fatal(err)
		}
		if res.BaseURL.String() != "https://mise.example.com" {
			t.Errorf("base URL = %s", res.BaseURL)
		}
		id, _, err := s.Authenticate(ctx, res.Token)
		if err != nil {
			t.Fatal(err)
		}
		if id.Role != RoleAdmin || id.Name != "Anna" || id.HouseholdName != "Home" || id.Timezone != "Europe/Vienna" {
			t.Errorf("identity = %+v", id)
		}
		if u, err := s.StoredBaseURL(ctx); err != nil || u.String() != "https://mise.example.com" {
			t.Errorf("stored base URL = %v, %v", u, err)
		}

		// Locked out afterwards, also for a fresh Service without the cache.
		in := setupInput()
		in.Username = "mallory"
		if _, err := s.Setup(ctx, in); !errors.Is(err, ErrSetupDone) {
			t.Errorf("second setup: %v", err)
		}
		fresh, _ := newService(t, db)
		if _, err := fresh.Setup(ctx, in); !errors.Is(err, ErrSetupDone) {
			t.Errorf("second setup, uncached: %v", err)
		}
	})
}

func TestSetupRace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		const n = 4
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				s, _ := newService(t, db)
				in := setupInput()
				in.Username = "admin" + string(rune('a'+i))
				_, errs[i] = s.Setup(ctx, in)
			})
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, ErrSetupDone):
				t.Errorf("unexpected error: %v", err)
			}
		}
		var households int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM households`).Scan(&households); err != nil {
			t.Fatal(err)
		}
		if ok != 1 || households != 1 {
			t.Errorf("%d setups succeeded, %d households; want 1 and 1", ok, households)
		}
	})
}

func TestSignIn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		if _, err := s.Setup(ctx, setupInput()); err != nil {
			t.Fatal(err)
		}
		tok, err := s.SignIn(ctx, "ANNA", testPassword, "ua")
		if err != nil {
			t.Fatalf("case-insensitive sign-in: %v", err)
		}
		if _, err := s.SignIn(ctx, "anna", "wrong password", "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("wrong password: %v", err)
		}
		if _, err := s.SignIn(ctx, "nobody", testPassword, "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("unknown user: %v", err)
		}
		id, _, err := s.Authenticate(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SignOut(ctx, id.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrNoSession) {
			t.Errorf("after sign-out: %v", err)
		}
		if _, _, err := s.Authenticate(ctx, "garbage"); !errors.Is(err, ErrNoSession) {
			t.Errorf("unknown token: %v", err)
		}
	})
}

func TestSessionSliding(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		res, err := s.Setup(ctx, setupInput())
		if err != nil {
			t.Fatal(err)
		}
		expiry := func() time.Time {
			var at store.Time
			if err := db.System().Read().Get(ctx, &at, `SELECT expires_at FROM sessions WHERE id = ?`, hashToken(res.Token)); err != nil {
				t.Fatal(err)
			}
			return at.Time
		}
		start := expiry()
		if want := clock.Now().Add(SessionTTL); !start.Equal(want) {
			t.Fatalf("initial expiry %v, want %v", start, want)
		}

		clock.Add(SessionTouchInterval - time.Second)
		if _, refreshed, err := s.Authenticate(ctx, res.Token); err != nil || refreshed {
			t.Fatalf("within touch interval: refreshed=%v err=%v", refreshed, err)
		}
		if !expiry().Equal(start) {
			t.Error("expiry written within the touch interval")
		}

		clock.Add(2 * time.Second)
		if _, refreshed, err := s.Authenticate(ctx, res.Token); err != nil || !refreshed {
			t.Fatalf("after touch interval: refreshed=%v err=%v", refreshed, err)
		}
		if want := clock.Now().Add(SessionTTL); !expiry().Equal(want) {
			t.Errorf("expiry %v, want slid to %v", expiry(), want)
		}

		clock.Add(SessionTTL - time.Second)
		if _, _, err := s.Authenticate(ctx, res.Token); err != nil {
			t.Fatalf("just before expiry: %v", err)
		}
		clock.Add(SessionTTL)
		if _, _, err := s.Authenticate(ctx, res.Token); !errors.Is(err, ErrNoSession) {
			t.Errorf("after expiry: %v", err)
		}
	})
}

// fixture is a household with an admin and a member, created directly.
type fixture struct {
	household uuid.UUID
	admin     uuid.UUID
	member    uuid.UUID
	sc        store.Scoped
}

func addHousehold(t *testing.T, db *store.DB, name string) fixture {
	t.Helper()
	ctx := t.Context()
	f := fixture{household: store.NewID(), admin: store.NewID(), member: store.NewID()}
	f.sc = db.ScopeFor(f.household)
	now := store.Now()
	err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if _, err := q.Exec(ctx, `INSERT INTO households (id, name, timezone, week_start, default_language, created_at, updated_at)
			VALUES (?, ?, 'UTC', 'mon', 'en', ?, ?)`, f.household, name, now, now); err != nil {
			return err
		}
		for _, u := range []struct {
			id   uuid.UUID
			name string
			role Role
		}{{f.admin, name + "-admin", RoleAdmin}, {f.member, name + "-member", RoleMember}} {
			if err := insertUser(ctx, q, newUser{ID: u.id, Username: u.name, Name: u.name, PasswordHash: testHash(), AvatarColor: "amber", At: now}); err != nil {
				return err
			}
			if err := insertMembership(ctx, q, f.household, u.id, u.role, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestInvites(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		f := addHousehold(t, db, "home")

		if _, err := s.CreateInvite(ctx, f.sc, f.member, RoleMember, ""); !errors.Is(err, ErrForbidden) {
			t.Errorf("member creating invite: %v", err)
		}
		tok, err := s.CreateInvite(ctx, f.sc, f.admin, RoleAdmin, "ben@example.com")
		if err != nil {
			t.Fatal(err)
		}
		info, err := s.InviteByToken(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}
		if info.HouseholdName != "home" || info.InviterName != "home-admin" || info.Role != RoleAdmin || info.Email != "ben@example.com" {
			t.Errorf("info = %+v", info)
		}
		pending, err := s.PendingInvites(ctx, f.sc)
		if err != nil || len(pending) != 1 {
			t.Fatalf("pending = %v, %v", pending, err)
		}

		in := AcceptInput{Name: "Ben", Username: "ben", Password: testPassword, UserAgent: "ua"}
		session, err := s.AcceptInvite(ctx, tok, in)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.Authenticate(ctx, session)
		if err != nil || id.Role != RoleAdmin || id.HouseholdID != f.household {
			t.Fatalf("new member identity = %+v, %v", id, err)
		}

		// Single use.
		in.Username = "ben2"
		if _, err := s.AcceptInvite(ctx, tok, in); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("second accept: %v", err)
		}
		if _, err := s.InviteByToken(ctx, tok); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("lookup after use: %v", err)
		}
		if pending, _ := s.PendingInvites(ctx, f.sc); len(pending) != 0 {
			t.Errorf("used invite still pending: %v", pending)
		}

		// Taken usernames are a field error and leave the invite usable.
		tok2, err := s.CreateInvite(ctx, f.sc, f.admin, RoleMember, "")
		if err != nil {
			t.Fatal(err)
		}
		in.Username = "BEN"
		var fe FieldErrors
		if _, err := s.AcceptInvite(ctx, tok2, in); !errors.As(err, &fe) || fe["username"] != "form.error.usernameTaken" {
			t.Errorf("taken username: %v", err)
		}
		if _, err := s.InviteByToken(ctx, tok2); err != nil {
			t.Errorf("invite consumed by failed accept: %v", err)
		}

		// Expiry.
		clock.Add(InviteTTL)
		if _, err := s.InviteByToken(ctx, tok2); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("expired lookup: %v", err)
		}
		in.Username = "carla"
		if _, err := s.AcceptInvite(ctx, tok2, in); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("expired accept: %v", err)
		}

		// Revocation.
		tok3, err := s.CreateInvite(ctx, f.sc, f.admin, RoleMember, "")
		if err != nil {
			t.Fatal(err)
		}
		pending, _ = s.PendingInvites(ctx, f.sc)
		if len(pending) != 1 {
			t.Fatalf("pending = %v", pending)
		}
		if err := s.RevokeInvite(ctx, f.sc, f.admin, pending[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InviteByToken(ctx, tok3); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("revoked lookup: %v", err)
		}
	})
}

func TestInviteAcceptRace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		tok, err := s.CreateInvite(ctx, f.sc, f.admin, RoleMember, "")
		if err != nil {
			t.Fatal(err)
		}
		const n = 4
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				_, errs[i] = s.AcceptInvite(ctx, tok, AcceptInput{Name: "X", Username: "user" + string(rune('a'+i)), Password: testPassword})
			})
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, ErrInviteInvalid):
				t.Errorf("unexpected error: %v", err)
			}
		}
		if ok != 1 {
			t.Errorf("%d accepts succeeded, want 1", ok)
		}
	})
}

func TestLastAdmin(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")

		if err := s.SetRole(ctx, f.sc, f.admin, f.admin, RoleMember); !errors.Is(err, ErrLastAdmin) {
			t.Errorf("demoting the only admin: %v", err)
		}
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.admin); !errors.Is(err, ErrSelf) {
			t.Errorf("removing yourself: %v", err)
		}
		if err := s.SetRole(ctx, f.sc, f.member, f.member, RoleAdmin); !errors.Is(err, ErrForbidden) {
			t.Errorf("member promoting themselves: %v", err)
		}
		if err := s.ResetPassword(ctx, f.sc, f.admin, f.admin, testPassword); !errors.Is(err, ErrSelf) {
			t.Errorf("admin reset of own password: %v", err)
		}

		// With a second admin, either can step down, but not both.
		if err := s.SetRole(ctx, f.sc, f.admin, f.member, RoleAdmin); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRole(ctx, f.sc, f.member, f.admin, RoleMember); err != nil {
			t.Fatalf("demoting one of two admins: %v", err)
		}
		if err := s.SetRole(ctx, f.sc, f.member, f.member, RoleMember); !errors.Is(err, ErrLastAdmin) {
			t.Errorf("demoting the remaining admin: %v", err)
		}
		members, err := s.Members(ctx, f.sc)
		if err != nil {
			t.Fatal(err)
		}
		admins := 0
		for _, m := range members {
			if m.Role == RoleAdmin {
				admins++
			}
		}
		if admins != 1 {
			t.Errorf("%d admins, want 1", admins)
		}
	})
}

func TestRemoveAndReset(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")

		tok, err := s.SignIn(ctx, "home-member", testPassword, "ua")
		if err != nil {
			t.Fatal(err)
		}
		const newPassword = "a brand new secret"
		if err := s.ResetPassword(ctx, f.sc, f.admin, f.member, newPassword); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrNoSession) {
			t.Errorf("session survived password reset: %v", err)
		}
		if _, err := s.SignIn(ctx, "home-member", testPassword, "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("old password still works: %v", err)
		}
		tok, err = s.SignIn(ctx, "home-member", newPassword, "ua")
		if err != nil {
			t.Fatal(err)
		}

		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrNoSession) {
			t.Errorf("session survived removal: %v", err)
		}
		if _, err := s.SignIn(ctx, "home-member", newPassword, "ua"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("removed member signed in: %v", err)
		}
		if _, err := s.Member(ctx, f.sc, f.member); !errors.Is(err, ErrNotFound) {
			t.Errorf("removed member still listed: %v", err)
		}
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); !errors.Is(err, ErrNotFound) {
			t.Errorf("removing twice: %v", err)
		}
	})
}

func TestAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		res, err := s.Setup(ctx, setupInput())
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.SignIn(ctx, "anna", testPassword, "phone")
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.Authenticate(ctx, res.Token)
		if err != nil {
			t.Fatal(err)
		}

		if err := s.UpdateProfile(ctx, id, Profile{Name: "Anna B", Email: "", AvatarColor: "sky"}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdatePreferences(ctx, id, Preferences{Theme: "dark", Language: "de"}); err != nil {
			t.Fatal(err)
		}
		var fe FieldErrors
		if err := s.UpdatePreferences(ctx, id, Preferences{Theme: "neon"}); !errors.As(err, &fe) {
			t.Errorf("bad theme accepted: %v", err)
		}
		id, _, _ = s.Authenticate(ctx, res.Token)
		if id.Name != "Anna B" || id.Email != "" || id.AvatarColor != "sky" || id.Theme != "dark" || id.Language != "de" {
			t.Errorf("identity after update = %+v", id)
		}

		sessions, err := s.Sessions(ctx, id)
		if err != nil || len(sessions) != 2 || !sessions[0].Current {
			t.Fatalf("sessions = %+v, %v", sessions, err)
		}

		if err := s.ChangePassword(ctx, id, "wrong", "another long password"); !errors.As(err, &fe) || fe["current_password"] == "" {
			t.Errorf("wrong current password: %v", err)
		}
		if err := s.ChangePassword(ctx, id, testPassword, "another long password"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Authenticate(ctx, other); !errors.Is(err, ErrNoSession) {
			t.Errorf("other session survived password change: %v", err)
		}
		if _, _, err := s.Authenticate(ctx, res.Token); err != nil {
			t.Errorf("current session ended by password change: %v", err)
		}

		other, _ = s.SignIn(ctx, "anna", "another long password", "tablet")
		oid, _, _ := s.Authenticate(ctx, other)
		if err := s.RevokeSession(ctx, id, oid.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Authenticate(ctx, other); !errors.Is(err, ErrNoSession) {
			t.Errorf("revoked session works: %v", err)
		}
		if err := s.RevokeSession(ctx, id, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoking unknown session: %v", err)
		}
	})
}
