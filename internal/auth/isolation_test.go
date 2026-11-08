package auth

import (
	"errors"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// TestHouseholdIsolation walks every Scoped method of this package against
// two households and checks that neither sees or changes the other's rows.
// Later phases add their repositories here (docs/plan.md, Multi-tenancy).
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
