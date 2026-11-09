package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Invite is a pending invite as listed for admins. The token is not
// recoverable; only CreateInvite returns it.
type Invite struct {
	ID            uuid.UUID
	Email         string
	Role          Role
	InvitedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// CreateInvite creates a single-use invite into the scoped household and
// returns its token. actor must be an active admin there.
func (s *Service) CreateInvite(ctx context.Context, sc store.Scoped, actor uuid.UUID, role Role, email string) (string, error) {
	email = clean(email)
	errs := FieldErrors{}
	if !role.Valid() {
		errs["role"] = "form.error.choice"
	}
	checkEmail(errs, "email", email)
	if err := errs.orNil(); err != nil {
		return "", err
	}

	token, hash := newToken()
	err := sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := requireAdmin(ctx, q, sc, actor); err != nil {
			return err
		}
		now := s.clock()
		_, err := q.Exec(ctx, `INSERT INTO invites (id, household_id, token_hash, email, role, invited_by, expires_at, used_at, used_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			store.NewID(), sc.HouseholdID(), hash, nullable(email), role, actor, store.T(now.Add(InviteTTL)), now, now)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// PendingInvites lists the household's unused, unexpired invites, newest
// first.
func (s *Service) PendingInvites(ctx context.Context, sc store.Scoped) ([]Invite, error) {
	var rows []struct {
		ID            uuid.UUID
		Email         *string
		Role          Role
		InvitedByName string
		CreatedAt     store.Time
		ExpiresAt     store.Time
	}
	err := sc.Read().Select(ctx, &rows, `
		SELECT i.id, i.email, i.role, u.name AS invited_by_name, i.created_at, i.expires_at
		FROM invites i JOIN users u ON u.id = i.invited_by
		WHERE i.household_id = ? AND i.used_at IS NULL AND i.expires_at > ?
		ORDER BY i.created_at DESC`, sc.HouseholdID(), s.clock())
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = Invite{ID: r.ID, Email: deref(r.Email), Role: r.Role, InvitedByName: r.InvitedByName, CreatedAt: r.CreatedAt.Time, ExpiresAt: r.ExpiresAt.Time}
	}
	return out, nil
}

// RevokeInvite deletes an unused invite of the scoped household.
func (s *Service) RevokeInvite(ctx context.Context, sc store.Scoped, actor, inviteID uuid.UUID) error {
	return sc.Tx(ctx, func(ctx context.Context, q store.Querier) error {
		if err := requireAdmin(ctx, q, sc, actor); err != nil {
			return err
		}
		r, err := q.Exec(ctx, `DELETE FROM invites WHERE household_id = ? AND id = ? AND used_at IS NULL`, sc.HouseholdID(), inviteID)
		if err != nil {
			return err
		}
		return mustAffect(r)
	})
}

// InviteInfo is what the accept page shows about a valid invite.
type InviteInfo struct {
	HouseholdName string
	InviterName   string
	InviterColor  string
	Email         string
	Role          Role
	ExpiresAt     time.Time
}

// InviteByToken looks up a valid invite. It returns ErrInviteInvalid for
// unknown, expired, revoked or used tokens.
func (s *Service) InviteByToken(ctx context.Context, token string) (InviteInfo, error) {
	var row struct {
		HouseholdName string
		InviterName   string
		InviterColor  string
		Email         *string
		Role          Role
		ExpiresAt     store.Time
	}
	err := s.db.System().Read().Get(ctx, &row, `
		SELECT h.name AS household_name, u.name AS inviter_name, u.avatar_color AS inviter_color,
		       i.email, i.role, i.expires_at
		FROM invites i
		JOIN households h ON h.id = i.household_id
		JOIN users u ON u.id = i.invited_by
		WHERE i.token_hash = ? AND i.used_at IS NULL AND i.expires_at > ?`, hashToken(token), s.clock())
	if errors.Is(err, sql.ErrNoRows) {
		return InviteInfo{}, ErrInviteInvalid
	}
	if err != nil {
		return InviteInfo{}, err
	}
	return InviteInfo{
		HouseholdName: row.HouseholdName, InviterName: row.InviterName, InviterColor: row.InviterColor,
		Email: deref(row.Email), Role: row.Role, ExpiresAt: row.ExpiresAt.Time,
	}, nil
}

// AcceptInput is the accept-invite form.
type AcceptInput struct {
	Name      string
	Username  string
	Email     string
	Password  string
	UserAgent string
}

// AcceptInvite creates an account and membership from a valid invite, marks
// the invite used and returns a session token for the new user. A token can
// be used once, also when two accepts race.
func (s *Service) AcceptInvite(ctx context.Context, token string, in AcceptInput) (string, error) {
	in.Name, in.Username, in.Email = clean(in.Name), clean(in.Username), clean(in.Email)
	errs := FieldErrors{}
	checkName(errs, "name", in.Name)
	checkUsername(errs, "username", in.Username)
	checkEmail(errs, "email", in.Email)
	checkNewPassword(errs, "password", in.Password)
	if err := errs.orNil(); err != nil {
		return "", err
	}
	// Reject unknown tokens before the expensive hash (see CompleteReset);
	// the transaction below checks the token again.
	if _, err := s.InviteByToken(ctx, token); err != nil {
		return "", err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return "", err
	}

	var session string
	err = s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		now := s.clock()
		var inv struct {
			ID          uuid.UUID
			HouseholdID uuid.UUID
			Role        Role
		}
		err := q.Get(ctx, &inv, `SELECT id, household_id, role FROM invites
			WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`, hashToken(token), now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInviteInvalid
		}
		if err != nil {
			return err
		}

		var members int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE household_id = ?`, inv.HouseholdID).Scan(&members); err != nil {
			return err
		}
		userID := store.NewID()
		if err := insertUser(ctx, q, newUser{
			ID: userID, Username: in.Username, Name: in.Name, Email: in.Email,
			PasswordHash: hash, AvatarColor: AvatarColors[members%len(AvatarColors)], At: now,
		}); err != nil {
			return err
		}
		if err := insertMembership(ctx, q, inv.HouseholdID, userID, inv.Role, now); err != nil {
			return err
		}
		// Conditional on still being unused: a concurrent accept that got
		// here first makes this affect no rows (Postgres re-checks the WHERE
		// after waiting for the row lock; SQLite has a single writer).
		r, err := q.Exec(ctx, `UPDATE invites SET used_at = ?, used_by = ?, updated_at = ?
			WHERE id = ? AND used_at IS NULL`, now, userID, now, inv.ID)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrInviteInvalid
		}
		session, err = s.createSession(ctx, q, userID, inv.HouseholdID, in.UserAgent)
		return err
	})
	return session, err
}

// mustAffect returns ErrNotFound when a statement changed no rows.
func mustAffect(r sql.Result) error {
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
