package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

const testIssuer = "https://id.example.com"

func external(sub string) ExternalIdentity {
	return ExternalIdentity{Issuer: testIssuer, Subject: sub}
}

// signInAs returns the identity of a fresh password session of username.
func signInAs(t *testing.T, s *Service, username string) (*Identity, string) {
	t.Helper()
	tok, err := s.SignIn(t.Context(), username, testPassword, "ua")
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Authenticate(t.Context(), tok)
	if err != nil {
		t.Fatal(err)
	}
	return id, tok
}

// TestSignInExternal: an identity signs in only as the account linked to
// it. An unlinked one is refused even when an account uses its email
// address, so a member who types a housemate's address into their own
// account cannot catch the housemate's first SSO sign-in.
func TestSignInExternal(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		setEmail(t, db, f.member, "admin@example.com", nil)
		setEmail(t, db, f.admin, "admin@example.com", nil)

		for name, ext := range map[string]ExternalIdentity{
			"unlinked":   external("sub-1"),
			"no subject": external(""),
		} {
			if _, err := s.SignInExternal(ctx, ext, "ua"); !errors.Is(err, ErrNoAccount) {
				t.Errorf("%s: %v, want ErrNoAccount", name, err)
			}
		}
		if linked, err := s.LinkedIdentity(ctx, f.member, testIssuer); err != nil || linked {
			t.Fatalf("linked by a sign-in: %v, %v", linked, err)
		}

		admin, _ := signInAs(t, s, "home-admin")
		if err := s.Link(ctx, admin, external("sub-1")); err != nil {
			t.Fatal(err)
		}
		tok, err := s.SignInExternal(ctx, external("sub-1"), "ua")
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.Authenticate(ctx, tok)
		if err != nil || id.UserID != f.admin || id.HouseholdID != f.household {
			t.Fatalf("identity %+v, %v", id, err)
		}
		// The same subject at another issuer is another identity.
		other := external("sub-1")
		other.Issuer = "https://other.example.com"
		if _, err := s.SignInExternal(ctx, other, "ua"); !errors.Is(err, ErrNoAccount) {
			t.Errorf("other issuer: %v", err)
		}
	})
}

// TestLink: a signed-in user links the identity they signed in with at
// the provider; one identity per provider and account, one account per
// identity.
func TestLink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		addHousehold(t, db, "home")
		admin, _ := signInAs(t, s, "home-admin")
		member, _ := signInAs(t, s, "home-member")

		if err := s.Link(ctx, member, external("sub-member")); err != nil {
			t.Fatal(err)
		}
		if err := s.Link(ctx, member, external("sub-member")); err != nil {
			t.Errorf("linking again: %v", err)
		}
		if err := s.Link(ctx, admin, external("sub-member")); !errors.Is(err, ErrIdentityLinkedElsewhere) {
			t.Errorf("identity of another account: %v", err)
		}
		if err := s.Link(ctx, member, external("sub-other")); !errors.Is(err, ErrOtherIdentityLinked) {
			t.Errorf("second identity of the issuer: %v", err)
		}
		if linked, err := s.LinkedIdentity(ctx, admin.UserID, testIssuer); err != nil || linked {
			t.Errorf("admin linked: %v, %v", linked, err)
		}
	})
}

func TestRemoveMemberDropsIdentity(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		member, _ := signInAs(t, s, "home-member")
		if err := s.Link(ctx, member, external("sub-1")); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); err != nil {
			t.Fatal(err)
		}
		if linked, err := s.LinkedIdentity(ctx, f.member, testIssuer); err != nil || linked {
			t.Errorf("link survived removal: %v, %v", linked, err)
		}
		if _, err := s.SignInExternal(ctx, external("sub-1"), "ua"); !errors.Is(err, ErrNoAccount) {
			t.Errorf("removed member signed in: %v", err)
		}
	})
}

func TestUnlink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		id, tok := signInAs(t, s, "home-member")
		if err := s.Link(ctx, id, external("sub-1")); err != nil {
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
