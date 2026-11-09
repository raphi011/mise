package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

const testIssuer = "https://id.example.com"

func external(sub, email string, verified bool) ExternalIdentity {
	return ExternalIdentity{Issuer: testIssuer, Subject: sub, Email: email, EmailVerified: verified}
}

func TestSignInExternal(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		setEmail(t, db, f.member, "Member@Example.com", nil)

		for name, tc := range map[string]struct {
			ext  ExternalIdentity
			want error
		}{
			"unverified": {external("sub-1", "member@example.com", false), ErrEmailUnverified},
			"unknown":    {external("sub-1", "stranger@example.com", true), ErrNoAccount},
			"no email":   {external("sub-1", "", true), ErrNoAccount},
		} {
			if _, err := s.SignInExternal(ctx, tc.ext, "ua"); !errors.Is(err, tc.want) {
				t.Errorf("%s: %v, want %v", name, err, tc.want)
			}
		}
		if linked, err := s.LinkedIdentity(ctx, f.member, testIssuer); err != nil || linked {
			t.Fatalf("linked before a successful sign-in: %v, %v", linked, err)
		}

		// First sign-in links by verified email, case-insensitively.
		tok, err := s.SignInExternal(ctx, external("sub-1", "member@EXAMPLE.com", true), "ua")
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.Authenticate(ctx, tok)
		if err != nil || id.UserID != f.member || id.HouseholdID != f.household {
			t.Fatalf("identity %+v, %v", id, err)
		}
		if linked, err := s.LinkedIdentity(ctx, f.member, testIssuer); err != nil || !linked {
			t.Fatalf("not linked after sign-in: %v, %v", linked, err)
		}
		// Linked: the email no longer matters.
		if _, err := s.SignInExternal(ctx, external("sub-1", "changed@example.com", false), "ua"); err != nil {
			t.Errorf("linked sign-in: %v", err)
		}
		// Another subject of the same issuer cannot take over the account.
		if _, err := s.SignInExternal(ctx, external("sub-2", "member@example.com", true), "ua"); !errors.Is(err, ErrOtherIdentityLinked) {
			t.Errorf("second subject: %v", err)
		}
		// The same subject at another issuer is another identity.
		other := external("sub-1", "nobody@example.com", true)
		other.Issuer = "https://other.example.com"
		if _, err := s.SignInExternal(ctx, other, "ua"); !errors.Is(err, ErrNoAccount) {
			t.Errorf("other issuer: %v", err)
		}
	})
}

func TestSignInExternalAmbiguous(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		setEmail(t, db, f.admin, "shared@example.com", nil)
		setEmail(t, db, f.member, "SHARED@example.com", nil)
		if _, err := s.SignInExternal(ctx, external("sub-1", "shared@example.com", true), "ua"); !errors.Is(err, ErrAmbiguousEmail) {
			t.Errorf("shared address: %v", err)
		}
		// A removed member's address no longer counts.
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); err != nil {
			t.Fatal(err)
		}
		tok, err := s.SignInExternal(ctx, external("sub-1", "shared@example.com", true), "ua")
		if err != nil {
			t.Fatal(err)
		}
		if id, _, err := s.Authenticate(ctx, tok); err != nil || id.UserID != f.admin {
			t.Errorf("signed in as %+v, %v", id, err)
		}
	})
}

func TestRemoveMemberDropsIdentity(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		setEmail(t, db, f.member, "member@example.com", nil)
		if _, err := s.SignInExternal(ctx, external("sub-1", "member@example.com", true), "ua"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); err != nil {
			t.Fatal(err)
		}
		if linked, err := s.LinkedIdentity(ctx, f.member, testIssuer); err != nil || linked {
			t.Errorf("link survived removal: %v, %v", linked, err)
		}
		if _, err := s.SignInExternal(ctx, external("sub-1", "member@example.com", true), "ua"); !errors.Is(err, ErrNoAccount) {
			t.Errorf("removed member signed in: %v", err)
		}
	})
}

func TestUnlink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		setEmail(t, db, f.member, "member@example.com", nil)
		tok, err := s.SignInExternal(ctx, external("sub-1", "member@example.com", true), "ua")
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.Authenticate(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}

		// Without a password, unlinking would lock the account out.
		setPasswordHash(t, db, f.member, "")
		if err := s.Unlink(ctx, id, testIssuer); !errors.Is(err, ErrNoPassword) {
			t.Errorf("unlink without password: %v", err)
		}
		setPasswordHash(t, db, f.member, testHash())
		if err := s.Unlink(ctx, id, testIssuer); err != nil {
			t.Fatal(err)
		}
		if linked, _ := s.LinkedIdentity(ctx, f.member, testIssuer); linked {
			t.Error("still linked after unlink")
		}
		if err := s.Unlink(ctx, id, testIssuer); !errors.Is(err, ErrNotFound) {
			t.Errorf("unlink twice: %v", err)
		}
		// Unlinking leaves the session alone.
		if _, _, err := s.Authenticate(ctx, tok); err != nil {
			t.Errorf("session ended by unlink: %v", err)
		}
	})
}

func setPasswordHash(t *testing.T, db *store.DB, user any, hash string) {
	t.Helper()
	err := db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, user)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
