package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// identityOf returns the Identity of a new session for user.
func identityOf(t *testing.T, s *Service, db *store.DB, user uuid.UUID) *Identity {
	t.Helper()
	var tok string
	err := db.System().Tx(t.Context(), func(ctx context.Context, q store.Querier) (err error) {
		tok, err = s.signInUser(ctx, q, user, "ua")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Authenticate(t.Context(), tok)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAPITokenLifecycle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		f := addHousehold(t, db, "home")
		id := identityOf(t, s, db, f.member)

		token, err := s.CreateToken(ctx, id, NewTokenInput{Name: " Agent ", Scope: ScopeRead})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(token, TokenPrefix) {
			t.Errorf("token %q lacks the prefix", token)
		}
		// Only the hash is stored.
		var stored int
		err = db.System().Read().QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE token_hash = ?`, token).Scan(&stored)
		if err != nil || stored != 0 {
			t.Fatalf("token stored in the clear: %d, %v", stored, err)
		}

		got, err := s.AuthenticateToken(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if got.UserID != f.member || got.HouseholdID != f.household || got.APIScope != ScopeRead || got.SessionID != "" || got.TokenID.String() == "" {
			t.Errorf("identity %+v", got)
		}
		if got.Role != RoleMember {
			t.Errorf("role %v", got.Role)
		}
		list, err := s.Tokens(ctx, id)
		if err != nil || len(list) != 1 || list[0].Name != "Agent" || list[0].Scope != ScopeRead || list[0].LastUsedAt == nil {
			t.Fatalf("tokens %+v, %v", list, err)
		}

		// Unknown, malformed and session tokens are refused.
		sessionTok, _ := newToken()
		for name, tok := range map[string]string{"unknown": TokenPrefix + "nope", "empty": "", "session token": sessionTok, "no prefix": strings.TrimPrefix(token, TokenPrefix)} {
			if _, err := s.AuthenticateToken(ctx, tok); !errors.Is(err, ErrNoToken) {
				t.Errorf("%s: %v", name, err)
			}
		}

		// Another user cannot see or revoke it.
		other := identityOf(t, s, db, f.admin)
		if l, _ := s.Tokens(ctx, other); len(l) != 0 {
			t.Errorf("admin sees member's tokens: %+v", l)
		}
		if err := s.RevokeToken(ctx, other, list[0].ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoke by other user: %v", err)
		}
		if err := s.RevokeToken(ctx, id, list[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, token); !errors.Is(err, ErrNoToken) {
			t.Errorf("revoked token: %v", err)
		}
		_ = clock
	})
}

func TestAPITokenExpiryAndRemoval(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		f := addHousehold(t, db, "home")
		id := identityOf(t, s, db, f.member)

		short, err := s.CreateToken(ctx, id, NewTokenInput{Name: "short", Scope: ScopeWrite, TTL: 24 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		forever, err := s.CreateToken(ctx, id, NewTokenInput{Name: "forever", Scope: ScopeWrite})
		if err != nil {
			t.Fatal(err)
		}
		clock.Add(25 * time.Hour)
		if _, err := s.AuthenticateToken(ctx, short); !errors.Is(err, ErrNoToken) {
			t.Errorf("expired token: %v", err)
		}
		if _, err := s.AuthenticateToken(ctx, forever); err != nil {
			t.Errorf("token without expiry: %v", err)
		}

		// A removed member's tokens are gone.
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, forever); !errors.Is(err, ErrNoToken) {
			t.Errorf("removed member's token: %v", err)
		}
	})
}

// An admin's password reset takes over an account that may be compromised:
// its tokens end with its sessions. Changing one's own password does not.
func TestPasswordResetEndsTokens(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		token, err := s.CreateToken(ctx, identityOf(t, s, db, f.member), NewTokenInput{Name: "agent", Scope: ScopeWrite})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ResetPassword(ctx, f.sc, f.admin, f.member, "a new password 123"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, token); !errors.Is(err, ErrNoToken) {
			t.Errorf("token survived an admin reset: %v", err)
		}
	})
}

func TestCreateTokenValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		id := identityOf(t, s, db, f.member)

		for name, in := range map[string]NewTokenInput{
			"no name":   {Scope: ScopeRead},
			"long name": {Name: strings.Repeat("x", maxTokenNameLen+1), Scope: ScopeRead},
			"no scope":  {Name: "a"},
			"bad scope": {Name: "a", Scope: "admin"},
		} {
			var fe FieldErrors
			if _, err := s.CreateToken(ctx, id, in); !errors.As(err, &fe) {
				t.Errorf("%s: %v", name, err)
			}
		}
		for i := range maxTokensPerUser {
			if _, err := s.CreateToken(ctx, id, NewTokenInput{Name: "t", Scope: ScopeRead}); err != nil {
				t.Fatalf("token %d: %v", i, err)
			}
		}
		if _, err := s.CreateToken(ctx, id, NewTokenInput{Name: "t", Scope: ScopeRead}); !errors.Is(err, ErrTooManyTokens) {
			t.Errorf("over the limit: %v", err)
		}
	})
}

func TestTokenScopeCan(t *testing.T) {
	for _, tc := range []struct {
		s     TokenScope
		write bool
		want  bool
	}{
		{ScopeRead, false, true}, {ScopeRead, true, false}, {ScopeWrite, false, true}, {ScopeWrite, true, true}, {"", false, false},
	} {
		if got := tc.s.Can(tc.write); got != tc.want {
			t.Errorf("%q.Can(%v) = %v", tc.s, tc.write, got)
		}
	}
}
