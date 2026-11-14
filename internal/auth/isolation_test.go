package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdIsolation walks every Scoped method of this package against
// two households and checks that neither sees or changes the other's rows.
// Later phases add their repositories here.
func TestHouseholdIsolation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		a := addHousehold(t, db, "a")
		b := addHousehold(t, db, "b")
		for _, f := range []fixture{a, b} {
			if _, err := s.CreateInvite(ctx, f.sc, f.admin, RoleMember, ""); err != nil {
				t.Fatal(err)
			}
		}
		shared := map[uuid.UUID]uuid.UUID{
			a.household: addSharedRecipe(t, db, a, "token-a"),
			b.household: addSharedRecipe(t, db, b, "token-b"),
		}
		tokens := map[uuid.UUID]string{a.household: "token-a", b.household: "token-b"}
		names := map[uuid.UUID]string{a.household: "a", b.household: "b"}

		for _, pair := range []struct{ own, other fixture }{{a, b}, {b, a}} {
			own, other := pair.own, pair.other

			members, err := s.Members(ctx, own.sc)
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != 2 {
				t.Errorf("Members: %d rows, want 2", len(members))
			}
			for _, m := range members {
				if m.UserID == other.admin || m.UserID == other.member {
					t.Errorf("Members returned other household's user %s", m.Name)
				}
			}

			// The household's settings are its own; another household's admin
			// cannot change them.
			if h, err := s.Household(ctx, own.sc); err != nil || h.Name != names[own.household] {
				t.Errorf("Household: %+v, %v", h, err)
			}
			if err := s.UpdateHousehold(ctx, own.sc, other.admin, HouseholdSettings{Name: "x", Timezone: "UTC", WeekStart: "mon", Language: "en"}); !errors.Is(err, ErrForbidden) {
				t.Errorf("UpdateHousehold by other household's admin: %v", err)
			}

			// A public link resolves to its own household's recipe only.
			if sh, err := s.ShareByToken(ctx, tokens[own.household]); err != nil ||
				sh.HouseholdID != own.household || sh.RecipeID != shared[own.household] {
				t.Errorf("ShareByToken(own link): %+v, %v", sh, err)
			}

			if _, err := s.Member(ctx, own.sc, other.member); !errors.Is(err, ErrNotFound) {
				t.Errorf("Member(other household's user): %v", err)
			}

			invites, err := s.PendingInvites(ctx, own.sc)
			if err != nil {
				t.Fatal(err)
			}
			otherInvites, err := s.PendingInvites(ctx, other.sc)
			if err != nil {
				t.Fatal(err)
			}
			if len(invites) != 1 || len(otherInvites) != 1 || invites[0].ID == otherInvites[0].ID {
				t.Fatalf("PendingInvites: own %v, other %v", invites, otherInvites)
			}

			// Writes aimed at the other household fail and change nothing.
			if err := s.RevokeInvite(ctx, own.sc, own.admin, otherInvites[0].ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("RevokeInvite(other household's invite): %v", err)
			}
			if err := s.SetRole(ctx, own.sc, own.admin, other.member, RoleAdmin); !errors.Is(err, ErrNotFound) {
				t.Errorf("SetRole(other household's user): %v", err)
			}
			if err := s.ResetPassword(ctx, own.sc, own.admin, other.member, "whatever long enough"); !errors.Is(err, ErrNotFound) {
				t.Errorf("ResetPassword(other household's user): %v", err)
			}
			if err := s.RemoveMember(ctx, own.sc, own.admin, other.member); !errors.Is(err, ErrNotFound) {
				t.Errorf("RemoveMember(other household's user): %v", err)
			}
			// An admin of one household has no rights in the other.
			if _, err := s.CreateInvite(ctx, own.sc, other.admin, RoleMember, ""); !errors.Is(err, ErrForbidden) {
				t.Errorf("CreateInvite by other household's admin: %v", err)
			}
		}

		for _, f := range []fixture{a, b} {
			if m, err := s.Member(ctx, f.sc, f.member); err != nil || m.Role != RoleMember {
				t.Errorf("member changed by the other household: %+v, %v", m, err)
			}
			if inv, _ := s.PendingInvites(ctx, f.sc); len(inv) != 1 {
				t.Errorf("invites changed by the other household: %v", inv)
			}
		}
		if _, err := s.SignIn(ctx, "a-member", testPassword, "ua"); err != nil {
			t.Errorf("password changed by the other household: %v", err)
		}
	})
}

// TestHouseholdSwitchAndLeave covers a user who belongs to two households:
// switching moves only the current session and only into households the
// user is an active member of; leaving follows the member-removal rules.
func TestHouseholdSwitchAndLeave(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, clock := newService(t, db)
		a := addHousehold(t, db, "a")
		b := addHousehold(t, db, "b")
		c := addHousehold(t, db, "c")

		// a-member also joins b, one minute later so a stays the oldest.
		_ = clock
		err := db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			return insertMembership(ctx, q, b.household, a.member, RoleMember, store.T(time.Now().Add(time.Minute)))
		})
		if err != nil {
			t.Fatal(err)
		}
		signIn := func() *Identity {
			t.Helper()
			tok, err := s.SignIn(ctx, "a-member", testPassword, "ua")
			if err != nil {
				t.Fatal(err)
			}
			id, _, err := s.Authenticate(ctx, tok)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		first, second := signIn(), signIn()
		if first.HouseholdID != a.household {
			t.Fatalf("sign-in household = %s, want the oldest membership", first.HouseholdID)
		}

		hs, err := s.Households(ctx, a.member)
		if err != nil || len(hs) != 2 || hs[0].ID != a.household || hs[1].ID != b.household {
			t.Fatalf("Households = %+v, %v", hs, err)
		}
		if hs, _ := s.Households(ctx, a.admin); len(hs) != 1 {
			t.Errorf("single-household user lists %d households", len(hs))
		}

		// Foreign household refused, session untouched.
		if err := s.SwitchHousehold(ctx, first, c.household); !errors.Is(err, ErrNotFound) {
			t.Errorf("Switch to foreign household: %v", err)
		}
		if err := s.SwitchHousehold(ctx, first, b.household); err != nil {
			t.Fatal(err)
		}
		current := func(id *Identity) uuid.UUID {
			t.Helper()
			var h uuid.UUID
			if err := db.System().Read().Get(ctx, &h, `SELECT household_id FROM sessions WHERE id = ?`, id.SessionID); err != nil {
				t.Fatal(err)
			}
			return h
		}
		if current(first) != b.household {
			t.Errorf("switched session in %s, want b", current(first))
		}
		if current(second) != a.household {
			t.Errorf("other session moved to %s", current(second))
		}

		// Leaving rules.
		if _, err := s.LeaveHousehold(ctx, first, c.household); !errors.Is(err, ErrNotFound) {
			t.Errorf("Leave foreign household: %v", err)
		}
		if _, err := s.LeaveHousehold(ctx, &Identity{UserID: a.admin, HouseholdID: a.household}, a.household); !errors.Is(err, ErrLastHousehold) {
			t.Errorf("Leave last household: %v", err)
		}
		// Make a-member the only admin of a: leaving a is then refused.
		if err := s.SetRole(ctx, a.sc, a.admin, a.member, RoleAdmin); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRole(ctx, a.sc, a.admin, a.admin, RoleMember); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LeaveHousehold(ctx, first, a.household); !errors.Is(err, ErrLastAdmin) {
			t.Errorf("Leave as last admin: %v", err)
		}
		if err := s.SetRole(ctx, a.sc, a.member, a.admin, RoleAdmin); err != nil {
			t.Fatal(err)
		}

		// Leave the active household b: the session moves to a, b's rows go.
		inB := *first
		inB.HouseholdID = b.household
		if _, err := s.CreateToken(ctx, &inB, NewTokenInput{Name: "t", Scope: ScopeRead}); err != nil {
			t.Fatalf("token: %v", err)
		}
		moved, err := s.LeaveHousehold(ctx, &inB, b.household)
		if err != nil || moved != a.household {
			t.Fatalf("Leave active household: moved %s, %v", moved, err)
		}
		if current(first) != a.household {
			t.Errorf("session after leaving in %s, want a", current(first))
		}
		if _, err := s.Member(ctx, b.sc, a.member); !errors.Is(err, ErrNotFound) {
			t.Errorf("still a member of b: %v", err)
		}
		var n int
		if err := db.System().Read().QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE household_id = ? AND user_id = ?`,
			b.household, a.member).Scan(&n); err != nil || n != 0 {
			t.Errorf("tokens left in b: %d, %v", n, err)
		}
		if err := s.SwitchHousehold(ctx, first, b.household); !errors.Is(err, ErrNotFound) {
			t.Errorf("Switch to household left: %v", err)
		}
		if _, _, err := s.Authenticate(ctx, mustToken(t, s, ctx)); err != nil {
			t.Errorf("sign-in after leaving: %v", err)
		}
	})
}

func mustToken(t *testing.T, s *Service, ctx context.Context) string {
	t.Helper()
	tok, err := s.SignIn(ctx, "a-member", testPassword, "ua")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestJoinInvite covers an existing user accepting an invite to a second
// household, including rejoining after leaving.
func TestJoinInvite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		ctx := t.Context()
		s, _ := newService(t, db)
		a := addHousehold(t, db, "a")
		b := addHousehold(t, db, "b")
		tok, err := s.SignIn(ctx, "a-member", testPassword, "ua")
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.Authenticate(ctx, tok)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := s.JoinInvite(ctx, id, "nope"); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("unknown token: %v", err)
		}
		invite, err := s.CreateInvite(ctx, b.sc, b.admin, RoleAdmin, "")
		if err != nil {
			t.Fatal(err)
		}
		hh, err := s.JoinInvite(ctx, id, invite)
		if err != nil || hh != b.household {
			t.Fatalf("JoinInvite = %s, %v", hh, err)
		}
		if m, err := s.Member(ctx, b.sc, a.member); err != nil || m.Role != RoleAdmin {
			t.Errorf("membership in b: %+v, %v", m, err)
		}
		if _, err := s.JoinInvite(ctx, id, invite); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("used invite: %v", err)
		}
		if id2, _, err := s.Authenticate(ctx, tok); err != nil || id2.HouseholdID != b.household {
			t.Errorf("session after join: %+v, %v", id2, err)
		}

		// Already a member: invite kept, nothing changes.
		again, err := s.CreateInvite(ctx, b.sc, b.admin, RoleMember, "")
		if err != nil {
			t.Fatal(err)
		}
		if hh, err := s.JoinInvite(ctx, id, again); !errors.Is(err, ErrAlreadyMember) || hh != b.household {
			t.Errorf("already a member: %s, %v", hh, err)
		}
		if inv, _ := s.PendingInvites(ctx, b.sc); len(inv) != 1 {
			t.Errorf("invite consumed by an existing member: %d pending", len(inv))
		}

		// Leave and rejoin with a member invite: role follows the invite.
		inB := *id
		inB.HouseholdID = b.household
		if _, err := s.LeaveHousehold(ctx, &inB, b.household); err != nil {
			t.Fatal(err)
		}
		if _, err := s.JoinInvite(ctx, &inB, again); err != nil {
			t.Fatal(err)
		}
		if m, err := s.Member(ctx, b.sc, a.member); err != nil || m.Role != RoleMember {
			t.Errorf("rejoined membership: %+v, %v", m, err)
		}
	})
}
