package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// ResetTTL is how long a password-reset link stays valid.
const ResetTTL = time.Hour

// ErrResetInvalid means the reset token is unknown, expired or used, or the
// account no longer has an active membership.
var ErrResetInvalid = errors.New("auth: password reset link invalid or expired")

// ResetTicket is a password reset to send: one account and its token.
type ResetTicket struct {
	UserID   uuid.UUID
	Username string
	Name     string
	Email    string
	// Language is the user's UI language, or the default language of their
	// household when they have none.
	Language string
	Token    string
}

// RequestPasswordReset creates a reset token for every account with this
// email address (compared case-insensitively) and an active membership.
// Several accounts may share an address; each gets its own ticket. No
// match is not an error: the caller answers the same either way.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) ([]ResetTicket, error) {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > maxEmailLen {
		return nil, nil
	}
	var tickets []ResetTicket
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		// Accounts are few on a self-hosted server; comparing in Go folds
		// non-ASCII addresses the same on both engines, which SQL lower()
		// does not.
		var rows []struct {
			ID                uuid.UUID
			Username          string
			Name              string
			Email             string
			Language          *string
			HouseholdLanguage string
		}
		if err := q.Select(ctx, &rows, `
			SELECT u.id, u.username, u.name, u.email, u.language, h.default_language AS household_language
			FROM users u
			JOIN memberships m ON m.user_id = u.id AND m.removed_at IS NULL
			JOIN households h ON h.id = m.household_id
			WHERE u.email IS NOT NULL AND u.email <> ''
			ORDER BY u.id, m.created_at`); err != nil {
			return err
		}
		now := s.clock()
		seen := map[uuid.UUID]bool{}
		for _, r := range rows {
			// One row per membership, oldest first: keep the first.
			if seen[r.ID] || !strings.EqualFold(r.Email, email) {
				continue
			}
			seen[r.ID] = true
			token, hash := newToken()
			if _, err := q.Exec(ctx, `INSERT INTO password_resets (id, token_hash, user_id, expires_at, used_at, created_at, updated_at)
				VALUES (?, ?, ?, ?, NULL, ?, ?)`, store.NewID(), hash, r.ID, store.T(now.Add(ResetTTL)), now, now); err != nil {
				return err
			}
			lang := r.HouseholdLanguage
			if r.Language != nil && *r.Language != "" {
				lang = *r.Language
			}
			tickets = append(tickets, ResetTicket{
				UserID: r.ID, Username: r.Username, Name: r.Name, Email: r.Email, Language: lang, Token: token,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tickets, nil
}

// ResetInfo is what the reset page shows about a valid token.
type ResetInfo struct {
	Username string
	Name     string
}

// resetRow is a valid reset with its account's oldest active household.
type resetRow struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Username    string
	Name        string
	HouseholdID uuid.UUID
}

// validReset loads an unused, unexpired reset whose account still has an
// active membership.
func (s *Service) validReset(ctx context.Context, q store.Reader, token string) (resetRow, error) {
	var row resetRow
	err := q.Get(ctx, &row, `
		SELECT r.id, r.user_id, u.username, u.name, m.household_id
		FROM password_resets r
		JOIN users u ON u.id = r.user_id
		JOIN memberships m ON m.user_id = r.user_id AND m.removed_at IS NULL
		WHERE r.token_hash = ? AND r.used_at IS NULL AND r.expires_at > ?
		ORDER BY m.created_at LIMIT 1`, hashToken(token), s.clock())
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrResetInvalid
	}
	return row, err
}

// ResetByToken looks up a valid reset token. It returns ErrResetInvalid
// for unknown, expired or used tokens.
func (s *Service) ResetByToken(ctx context.Context, token string) (ResetInfo, error) {
	row, err := s.validReset(ctx, s.db.System().Read(), token)
	if err != nil {
		return ResetInfo{}, err
	}
	return ResetInfo{Username: row.Username, Name: row.Name}, nil
}

// CompleteReset sets a new password with a valid reset token. It uses the
// token up, drops the account's other reset tokens, ends all its sessions
// and returns a new session token for this device and the account's ID (so
// the caller can end its live streams).
func (s *Service) CompleteReset(ctx context.Context, token, password, userAgent string) (string, uuid.UUID, error) {
	errs := FieldErrors{}
	checkNewPassword(errs, "password", password)
	if err := errs.orNil(); err != nil {
		return "", uuid.Nil, err
	}
	// Hashing is deliberately expensive: reject unknown tokens with a cheap
	// read first, so the endpoint cannot be used to burn CPU and memory.
	// The transaction below checks the token again.
	if _, err := s.validReset(ctx, s.db.System().Read(), token); err != nil {
		return "", uuid.Nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", uuid.Nil, err
	}
	var session string
	var userID uuid.UUID
	err = s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		row, err := s.validReset(ctx, q.Reader, token)
		if err != nil {
			return err
		}
		userID = row.UserID
		now := s.clock()
		// Conditional on still being unused, so two submits of one link
		// cannot both succeed (see AcceptInvite).
		r, err := q.Exec(ctx, `UPDATE password_resets SET used_at = ?, updated_at = ? WHERE id = ? AND used_at IS NULL`, now, now, row.ID)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrResetInvalid
		}
		if _, err := q.Exec(ctx, `DELETE FROM password_resets WHERE user_id = ? AND id <> ?`, row.UserID, row.ID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, now, row.UserID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, row.UserID); err != nil {
			return err
		}
		// A reset recovers an account that may be compromised: tokens go too.
		if _, err := q.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, row.UserID); err != nil {
			return err
		}
		session, err = s.createSession(ctx, q, row.UserID, row.HouseholdID, userAgent)
		return err
	})
	return session, userID, err
}

// dropResets deletes the account's outstanding reset tokens. Every change
// that settles who controls the account (a new password, a different email
// address) calls it, so an older link cannot undo that change.
func dropResets(ctx context.Context, q store.Querier, userID uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM password_resets WHERE user_id = ?`, userID)
	return err
}
