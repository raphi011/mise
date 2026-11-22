package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

const (
	testVerifier = "verifier-verifier-verifier-verifier-verifier"
	testCallback = "https://app.example/cb"
)

func testChallenge() string {
	sum := sha256.Sum256([]byte(testVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// grant registers a client, approves it as id and redeems the code.
func grant(t *testing.T, s *Service, id *Identity, scope TokenScope) (OAuthClient, TokenPair) {
	t.Helper()
	ctx := t.Context()
	c, err := s.RegisterClient(ctx, "  Test\napp ", []string{testCallback})
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.Authorize(ctx, id, AuthorizeInput{ClientID: c.ID, RedirectURI: testCallback, CodeChallenge: testChallenge(), Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := s.ExchangeCode(ctx, c.ID, code, testCallback, testVerifier)
	if err != nil {
		t.Fatal(err)
	}
	return c, pair
}

func TestOAuthGrantLifecycle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		f := addHousehold(t, db, "home")
		id := identityOf(t, s, db, f.member)

		c, pair := grant(t, s, id, ScopeWrite)
		if c.Name != "Test app" {
			t.Errorf("client name %q", c.Name)
		}
		got, err := s.AuthenticateToken(ctx, pair.AccessToken)
		if err != nil || got.UserID != f.member || got.HouseholdID != f.household || got.APIScope != ScopeWrite {
			t.Fatalf("identity %+v, %v", got, err)
		}
		var stored int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM oauth_grants WHERE access_hash = ? OR refresh_hash = ?`,
			pair.AccessToken, pair.RefreshToken).Scan(&stored); err != nil || stored != 0 {
			t.Fatalf("tokens stored in the clear: %d, %v", stored, err)
		}
		grants, err := s.OAuthGrants(ctx, id)
		if err != nil || len(grants) != 1 || grants[0].ClientName != "Test app" || grants[0].LastUsedAt == nil {
			t.Fatalf("grants %+v, %v", grants, err)
		}

		// Access tokens expire; the refresh token outlives them.
		clock.Add(AccessTTL)
		if _, err := s.AuthenticateToken(ctx, pair.AccessToken); !errors.Is(err, ErrNoToken) {
			t.Errorf("expired access token: %v", err)
		}
		next, err := s.RefreshOAuth(ctx, c.ID, pair.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, next.AccessToken); err != nil {
			t.Errorf("refreshed access token: %v", err)
		}
		clock.Add(RefreshTTL)
		if _, err := s.RefreshOAuth(ctx, c.ID, next.RefreshToken); !errors.Is(err, ErrOAuthGrant) {
			t.Errorf("expired refresh token: %v", err)
		}

		// Another member cannot revoke it; its member can.
		_, pair = grant(t, s, id, ScopeRead)
		grants, _ = s.OAuthGrants(ctx, id)
		other := identityOf(t, s, db, f.admin)
		if err := s.RevokeOAuthGrant(ctx, other, grants[0].ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoke by other member: %v", err)
		}
		if err := s.RevokeOAuthGrant(ctx, id, grants[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, pair.AccessToken); !errors.Is(err, ErrNoToken) {
			t.Errorf("revoked grant: %v", err)
		}
	})
}

func TestOAuthCodeChecks(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		f := addHousehold(t, db, "home")
		id := identityOf(t, s, db, f.member)
		c, err := s.RegisterClient(ctx, "", []string{testCallback, "http://127.0.0.1:8000/cb"})
		if err != nil {
			t.Fatal(err)
		}
		if c.Name != "MCP client" {
			t.Errorf("default name %q", c.Name)
		}
		// Loopback redirects match on any port, others exactly.
		for uri, want := range map[string]bool{
			testCallback: true, testCallback + "/x": false, "http://127.0.0.1:9999/cb": true, "http://127.0.0.1:9999/other": false,
			"http://localhost:8000/cb": false,
		} {
			if c.RedirectAllowed(uri) != want {
				t.Errorf("RedirectAllowed(%s) = %v", uri, !want)
			}
		}
		in := AuthorizeInput{ClientID: c.ID, RedirectURI: testCallback, CodeChallenge: testChallenge(), Scope: ScopeRead}
		for name, mod := range map[string]func(*AuthorizeInput){
			"unregistered redirect": func(in *AuthorizeInput) { in.RedirectURI = "https://evil.example/cb" },
			"plain challenge":       func(in *AuthorizeInput) { in.CodeChallenge = "short" },
			"unknown client":        func(in *AuthorizeInput) { in.ClientID = uuid.New() },
		} {
			bad := in
			mod(&bad)
			if _, err := s.Authorize(ctx, id, bad); err == nil {
				t.Errorf("%s: authorized", name)
			}
		}

		code, err := s.Authorize(ctx, id, in)
		if err != nil {
			t.Fatal(err)
		}
		other, _ := s.RegisterClient(ctx, "other", []string{testCallback})
		for name, try := range map[string]func() error{
			"other client": func() error { _, err := s.ExchangeCode(ctx, other.ID, code, testCallback, testVerifier); return err },
			"other redirect": func() error {
				_, err := s.ExchangeCode(ctx, c.ID, code, "http://127.0.0.1:8000/cb", testVerifier)
				return err
			},
			"wrong verifier": func() error {
				_, err := s.ExchangeCode(ctx, c.ID, code, testCallback, strings.Repeat("x", 43))
				return err
			},
			"short verifier":  func() error { _, err := s.ExchangeCode(ctx, c.ID, code, testCallback, "x"); return err },
			"refresh as code": func() error { _, err := s.RefreshOAuth(ctx, c.ID, code); return err },
		} {
			if err := try(); !errors.Is(err, ErrOAuthGrant) {
				t.Errorf("%s: %v", name, err)
			}
		}
		clock.Add(CodeTTL)
		if _, err := s.ExchangeCode(ctx, c.ID, code, testCallback, testVerifier); !errors.Is(err, ErrOAuthGrant) {
			t.Errorf("expired code: %v", err)
		}
	})
}

// TestOAuthGrantsEndWithMembership checks that removing a member, an
// admin setting their password and a password reset end their grants like
// their API tokens.
func TestOAuthGrantsEndWithMembership(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		f := addHousehold(t, db, "home")
		id := identityOf(t, s, db, f.member)

		_, pair := grant(t, s, id, ScopeRead)
		if err := s.ResetPassword(ctx, f.sc, f.admin, f.member, "a brand new passphrase"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, pair.AccessToken); !errors.Is(err, ErrNoToken) {
			t.Errorf("after password set by admin: %v", err)
		}

		id = identityOf(t, s, db, f.member)
		c, pair := grant(t, s, id, ScopeRead)
		if err := s.RemoveMember(ctx, f.sc, f.admin, f.member); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthenticateToken(ctx, pair.AccessToken); !errors.Is(err, ErrNoToken) {
			t.Errorf("after removal: %v", err)
		}
		if _, err := s.RefreshOAuth(ctx, c.ID, pair.RefreshToken); !errors.Is(err, ErrOAuthGrant) {
			t.Errorf("refresh after removal: %v", err)
		}
	})
}

// TestOAuthGrantIsolation checks that grants are listed and revoked only
// in their household.
func TestOAuthGrantIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		a := addHousehold(t, db, "a")
		b := addHousehold(t, db, "b")
		inA := identityOf(t, s, db, a.member)
		inB := identityOf(t, s, db, b.member)
		grant(t, s, inA, ScopeRead)
		_, pairB := grant(t, s, inB, ScopeRead)

		listA, err := s.OAuthGrants(ctx, inA)
		if err != nil || len(listA) != 1 {
			t.Fatalf("grants in a: %+v, %v", listA, err)
		}
		listB, _ := s.OAuthGrants(ctx, inB)
		if listA[0].ID == listB[0].ID {
			t.Error("households share a grant")
		}
		if err := s.RevokeOAuthGrant(ctx, inA, listB[0].ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoke b's grant from a: %v", err)
		}
		got, err := s.AuthenticateToken(ctx, pairB.AccessToken)
		if err != nil || got.HouseholdID != b.household {
			t.Errorf("b's token: %+v, %v", got, err)
		}
	})
}
