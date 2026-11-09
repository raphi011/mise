package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// setEmail gives a fixture user an email address and UI language.
func setEmail(t *testing.T, db *store.DB, user uuid.UUID, email string, language *string) {
	t.Helper()
	err := db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE users SET email = ?, language = ? WHERE id = ?`, email, language, user)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestPasswordReset(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		home := addHousehold(t, db, "home")
		other := addHousehold(t, db, "other")
		de := "de"
		// Two accounts share an address; letter case differs.
		setEmail(t, db, home.admin, "Anna@Example.com", nil)
		setEmail(t, db, other.member, "anna@example.com", &de)
		setEmail(t, db, home.member, "ben@example.com", nil)

		tickets, err := s.RequestPasswordReset(ctx, " anna@EXAMPLE.com ")
		if err != nil {
			t.Fatal(err)
		}
		if len(tickets) != 2 {
			t.Fatalf("tickets = %+v", tickets)
		}
		slices.SortFunc(tickets, func(a, b ResetTicket) int { return len(a.Username) - len(b.Username) })
		if tickets[0].Username != "home-admin" || tickets[0].Language != "en" || tickets[0].Email != "Anna@Example.com" {
			t.Errorf("first ticket %+v", tickets[0])
		}
		if tickets[1].Username != "other-member" || tickets[1].Language != "de" {
			t.Errorf("second ticket %+v (user language should win)", tickets[1])
		}
		if tickets[0].Token == tickets[1].Token || tickets[0].Token == "" {
			t.Error("tickets share or lack a token")
		}

		// Unknown addresses and removed members get nothing, without error.
		if got, err := s.RequestPasswordReset(ctx, "nobody@example.com"); err != nil || len(got) != 0 {
			t.Errorf("unknown address: %v, %v", got, err)
		}
		if err := s.RemoveMember(ctx, home.sc, home.admin, home.member); err != nil {
			t.Fatal(err)
		}
		if got, err := s.RequestPasswordReset(ctx, "ben@example.com"); err != nil || len(got) != 0 {
			t.Errorf("removed member: %v, %v", got, err)
		}
	})
}

func TestCompleteReset(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		f := addHousehold(t, db, "home")
		setEmail(t, db, f.member, "ben@example.com", nil)
		old, err := s.SignIn(ctx, "home-member", testPassword, "ua")
		if err != nil {
			t.Fatal(err)
		}
		first, err := s.RequestPasswordReset(ctx, "ben@example.com")
		if err != nil || len(first) != 1 {
			t.Fatal(first, err)
		}
		second, err := s.RequestPasswordReset(ctx, "ben@example.com")
		if err != nil || len(second) != 1 {
			t.Fatal(second, err)
		}
		info, err := s.ResetByToken(ctx, second[0].Token)
		if err != nil || info.Username != "home-member" {
			t.Fatalf("info %+v, %v", info, err)
		}

		var fe FieldErrors
		if _, _, err := s.CompleteReset(ctx, second[0].Token, "short", "ua"); !errors.As(err, &fe) || fe["password"] == "" {
			t.Errorf("short password: %v", err)
		}
		const newPassword = "a brand new secret"
		session, userID, err := s.CompleteReset(ctx, second[0].Token, newPassword, "phone")
		if err != nil {
			t.Fatal(err)
		}
		if userID != f.member {
			t.Errorf("user %v, want %v", userID, f.member)
		}
		if id, _, err := s.Authenticate(ctx, session); err != nil || id.UserID != f.member || id.HouseholdID != f.household {
			t.Errorf("new session %+v, %v", id, err)
		}
		if _, _, err := s.Authenticate(ctx, old); !errors.Is(err, ErrNoSession) {
			t.Errorf("old session survived the reset: %v", err)
		}
		if _, err := s.SignIn(ctx, "home-member", newPassword, "ua"); err != nil {
			t.Errorf("new password: %v", err)
		}
		// The token is used up, and the older one went with it.
		for _, tok := range []string{second[0].Token, first[0].Token} {
			if _, _, err := s.CompleteReset(ctx, tok, "yet another secret", "ua"); !errors.Is(err, ErrResetInvalid) {
				t.Errorf("reuse: %v", err)
			}
		}

		// Expiry.
		third, err := s.RequestPasswordReset(ctx, "ben@example.com")
		if err != nil || len(third) != 1 {
			t.Fatal(third, err)
		}
		clock.Add(ResetTTL)
		if _, err := s.ResetByToken(ctx, third[0].Token); !errors.Is(err, ErrResetInvalid) {
			t.Errorf("expired: %v", err)
		}
		if _, err := s.ResetByToken(ctx, "not-a-token"); !errors.Is(err, ErrResetInvalid) {
			t.Errorf("unknown token: %v", err)
		}
	})
}

// TestAccountChangesDropResets: a reset link must not undo a change that
// settled who controls the account. Changing only the name, or the email's
// case, keeps the link.
func TestAccountChangesDropResets(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, s *Service, f fixture, id *Identity, email string) error
		keeps  bool
	}{
		{"new email", func(t *testing.T, s *Service, f fixture, id *Identity, email string) error {
			return s.UpdateProfile(t.Context(), id, Profile{Name: "Ben", Email: "ben@elsewhere.example", AvatarColor: AvatarColors[0]})
		}, false},
		{"email removed", func(t *testing.T, s *Service, f fixture, id *Identity, email string) error {
			return s.UpdateProfile(t.Context(), id, Profile{Name: "Ben", AvatarColor: AvatarColors[0]})
		}, false},
		{"same email, other case", func(t *testing.T, s *Service, f fixture, id *Identity, email string) error {
			return s.UpdateProfile(t.Context(), id, Profile{Name: "Benjamin", Email: strings.ToUpper(email), AvatarColor: AvatarColors[0]})
		}, true},
		{"password change", func(t *testing.T, s *Service, f fixture, id *Identity, email string) error {
			return s.ChangePassword(t.Context(), id, testPassword, "a brand new secret")
		}, false},
		{"admin reset", func(t *testing.T, s *Service, f fixture, id *Identity, email string) error {
			return s.ResetPassword(t.Context(), f.sc, f.admin, f.member, "a brand new secret")
		}, false},
	}
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		for i, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				ctx := t.Context()
				s, _ := newService(t, db)
				name, email := fmt.Sprintf("h%d", i), fmt.Sprintf("ben%d@example.com", i)
				f := addHousehold(t, db, name)
				setEmail(t, db, f.member, email, nil)
				session, err := s.SignIn(ctx, name+"-member", testPassword, "ua")
				if err != nil {
					t.Fatal(err)
				}
				id, _, err := s.Authenticate(ctx, session)
				if err != nil {
					t.Fatal(err)
				}
				tickets, err := s.RequestPasswordReset(ctx, email)
				if err != nil || len(tickets) != 1 {
					t.Fatal(tickets, err)
				}
				if err := c.change(t, s, f, id, email); err != nil {
					t.Fatal(err)
				}
				_, err = s.ResetByToken(ctx, tickets[0].Token)
				if c.keeps && err != nil {
					t.Errorf("link dropped: %v", err)
				}
				if !c.keeps && !errors.Is(err, ErrResetInvalid) {
					t.Errorf("link survived: %v", err)
				}
			})
		}
	})
}
