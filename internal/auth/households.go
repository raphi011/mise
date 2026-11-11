package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// ErrLastHousehold means the user tried to leave the only household they
// belong to; an account always has one.
var ErrLastHousehold = errors.New("auth: cannot leave your last household")

// HouseholdInfo is one of the user's active households.
type HouseholdInfo struct {
	ID   uuid.UUID
	Name string
	Role Role
}

// Households lists the user's active households, oldest membership first
// (the order sign-in picks from).
func (s *Service) Households(ctx context.Context, userID uuid.UUID) ([]HouseholdInfo, error) {
	var rows []struct {
		ID   uuid.UUID
		Name string
		Role Role
	}
	err := s.db.System().Read().Select(ctx, &rows, `
		SELECT h.id, h.name, m.role
		FROM memberships m JOIN households h ON h.id = m.household_id
		WHERE m.user_id = ? AND m.removed_at IS NULL
		ORDER BY m.created_at, h.name`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]HouseholdInfo, len(rows))
	for i, r := range rows {
		out[i] = HouseholdInfo{ID: r.ID, Name: r.Name, Role: r.Role}
	}
	return out, nil
}

// SwitchHousehold moves the identity's session to another household the user
// is an active member of. Only this session changes; the user's other
// devices keep theirs. It returns ErrNotFound for any other household.
func (s *Service) SwitchHousehold(ctx context.Context, id *Identity, householdID uuid.UUID) error {
	if id.SessionID == "" {
		return ErrNotFound
	}
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		return s.moveSession(ctx, q, id, householdID)
	})
}

func (s *Service) moveSession(ctx context.Context, q store.Querier, id *Identity, householdID uuid.UUID) error {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM memberships
		WHERE household_id = ? AND user_id = ? AND removed_at IS NULL`, householdID, id.UserID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	r, err := q.Exec(ctx, `UPDATE sessions SET household_id = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		householdID, s.clock(), id.SessionID, id.UserID)
	if err != nil {
		return err
	}
	return mustAffect(r)
}

// LeaveHousehold ends the user's own membership in a household. Like member
// removal, the membership stays with removed_at set, and the member's
// sessions, API tokens and open invites for that household are deleted. It
// returns ErrNotFound for a household the user is not an active member of,
// ErrLastAdmin when they are its only admin and ErrLastHousehold when it is
// their only one. When it is the household of the identity's session, the
// session moves to the user's oldest remaining one; the returned ID is that
// household, or uuid.Nil when the session stayed where it was.
func (s *Service) LeaveHousehold(ctx context.Context, id *Identity, householdID uuid.UUID) (uuid.UUID, error) {
	sc := s.db.ScopeFor(householdID)
	var moved uuid.UUID
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		role, err := activeRole(ctx, q, sc, id.UserID)
		if err != nil {
			return err
		}
		var others []uuid.UUID
		if err := q.Select(ctx, &others, `SELECT household_id FROM memberships
			WHERE user_id = ? AND household_id <> ? AND removed_at IS NULL ORDER BY created_at, household_id`,
			id.UserID, householdID); err != nil {
			return err
		}
		if len(others) == 0 {
			return ErrLastHousehold
		}
		if role == RoleAdmin {
			n, err := countAdmins(ctx, q, sc)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		// Move the current session out first: the deletes below would end it.
		if id.HouseholdID == householdID && id.SessionID != "" {
			if err := s.moveSession(ctx, q, id, others[0]); err != nil {
				return err
			}
			moved = others[0]
		}
		now := s.clock()
		if _, err := q.Exec(ctx, `UPDATE memberships SET removed_at = ?, updated_at = ? WHERE household_id = ? AND user_id = ?`,
			now, now, householdID, id.UserID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM sessions WHERE household_id = ? AND user_id = ?`, householdID, id.UserID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM api_tokens WHERE household_id = ? AND user_id = ?`, householdID, id.UserID); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `DELETE FROM invites WHERE household_id = ? AND invited_by = ? AND used_at IS NULL`, householdID, id.UserID)
		return err
	})
	return moved, err
}
