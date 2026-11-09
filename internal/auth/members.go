package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Member is an active member of a household.
type Member struct {
	UserID      uuid.UUID
	Username    string
	Name        string
	AvatarColor string
	Role        Role
	JoinedAt    time.Time
}

const memberColumns = `u.id AS user_id, u.username, u.name, u.avatar_color, m.role, m.created_at AS joined_at`

type memberRow struct {
	UserID      uuid.UUID
	Username    string
	Name        string
	AvatarColor string
	Role        Role
	JoinedAt    store.Time
}

func (r memberRow) member() Member {
	return Member{UserID: r.UserID, Username: r.Username, Name: r.Name, AvatarColor: r.AvatarColor, Role: r.Role, JoinedAt: r.JoinedAt.Time}
}

// Members lists the scoped household's active members in joining order.
func (s *Service) Members(ctx context.Context, sc store.Scoped) ([]Member, error) {
	var rows []memberRow
	err := sc.Read().Select(ctx, &rows, `SELECT `+memberColumns+`
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.household_id = ? AND m.removed_at IS NULL
		ORDER BY m.created_at, u.name`, sc.HouseholdID())
	if err != nil {
		return nil, err
	}
	out := make([]Member, len(rows))
	for i, r := range rows {
		out[i] = r.member()
	}
	return out, nil
}

// Member returns one active member of the scoped household, or ErrNotFound.
func (s *Service) Member(ctx context.Context, sc store.Scoped, userID uuid.UUID) (Member, error) {
	var r memberRow
	err := sc.Read().Get(ctx, &r, `SELECT `+memberColumns+`
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.household_id = ? AND m.user_id = ? AND m.removed_at IS NULL`, sc.HouseholdID(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, err
	}
	return r.member(), nil
}

// activeRole returns the role of an active member, or ErrNotFound.
func activeRole(ctx context.Context, q store.Querier, sc store.Scoped, userID uuid.UUID) (Role, error) {
	var role Role
	err := q.Get(ctx, &role, `SELECT role FROM memberships WHERE household_id = ? AND user_id = ? AND removed_at IS NULL`,
		sc.HouseholdID(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// requireAdmin checks that actor is an active admin of the household. The
// middleware checks this too; repeating it here keeps the rule next to the
// writes.
func requireAdmin(ctx context.Context, q store.Querier, sc store.Scoped, actor uuid.UUID) error {
	role, err := activeRole(ctx, q, sc, actor)
	if errors.Is(err, ErrNotFound) || (err == nil && role != RoleAdmin) {
		return ErrForbidden
	}
	return err
}

func countAdmins(ctx context.Context, q store.Querier, sc store.Scoped) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE household_id = ? AND role = 'admin' AND removed_at IS NULL`,
		sc.HouseholdID()).Scan(&n)
	return n, err
}

// SetRole changes a member's role. It returns ErrLastAdmin when that would
// leave the household without an admin.
func (s *Service) SetRole(ctx context.Context, sc store.Scoped, actor, userID uuid.UUID, role Role) error {
	if !role.Valid() {
		return FieldErrors{"role": "form.error.choice"}
	}
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		if err := requireAdmin(ctx, q, sc, actor); err != nil {
			return err
		}
		current, err := activeRole(ctx, q, sc, userID)
		if err != nil {
			return err
		}
		if current == role {
			return nil
		}
		if current == RoleAdmin {
			n, err := countAdmins(ctx, q, sc)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		_, err = q.Exec(ctx, `UPDATE memberships SET role = ?, updated_at = ? WHERE household_id = ? AND user_id = ?`,
			role, s.clock(), sc.HouseholdID(), userID)
		return err
	})
}

// ResetPassword lets an admin set a new password for another member, and
// ends all of the member's sessions. The password is account-wide, so it
// returns ErrSharedAccount when the member also belongs to another household:
// otherwise one household's admin could take over the account in the other.
func (s *Service) ResetPassword(ctx context.Context, sc store.Scoped, actor, userID uuid.UUID, password string) error {
	if actor == userID {
		return ErrSelf
	}
	errs := FieldErrors{}
	checkNewPassword(errs, "password", password)
	if err := errs.orNil(); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := requireAdmin(ctx, q, sc, actor); err != nil {
			return err
		}
		if _, err := activeRole(ctx, q, sc, userID); err != nil {
			return err
		}
		var elsewhere int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM memberships
			WHERE user_id = ? AND household_id <> ? AND removed_at IS NULL`, userID, sc.HouseholdID()).Scan(&elsewhere); err != nil {
			return err
		}
		if elsewhere > 0 {
			return ErrSharedAccount
		}
		now := s.clock()
		// users and sessions are account-wide; the checks above limit this
		// to accounts that belong to the scoped household only.
		if _, err := q.Exec(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, now, userID); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	})
}

// RemoveMember removes a member from the household: the membership stays
// with removed_at set so attribution keeps working, their sessions end and
// their pending invites are revoked. Admins cannot remove themselves or the
// last admin.
func (s *Service) RemoveMember(ctx context.Context, sc store.Scoped, actor, userID uuid.UUID) error {
	if actor == userID {
		return ErrSelf
	}
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := sc.LockHousehold(ctx, q); err != nil {
			return err
		}
		if err := requireAdmin(ctx, q, sc, actor); err != nil {
			return err
		}
		role, err := activeRole(ctx, q, sc, userID)
		if err != nil {
			return err
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
		now := s.clock()
		hh := sc.HouseholdID()
		if _, err := q.Exec(ctx, `UPDATE memberships SET removed_at = ?, updated_at = ? WHERE household_id = ? AND user_id = ?`,
			now, now, hh, userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM sessions WHERE household_id = ? AND user_id = ?`, hh, userID); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `DELETE FROM invites WHERE household_id = ? AND invited_by = ? AND used_at IS NULL`, hh, userID)
		return err
	})
}
