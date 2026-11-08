package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// createSession inserts a session for the user in the household and returns
// its token. The database only sees the token's hash.
func (s *Service) createSession(ctx context.Context, q store.Querier, userID, householdID uuid.UUID, userAgent string) (string, error) {
	token, hash := newToken()
	now := s.clock()
	_, err := q.Exec(ctx, `INSERT INTO sessions (id, household_id, user_id, user_agent, expires_at, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hash, householdID, userID, truncate(userAgent, maxUserAgent), store.T(now.Add(SessionTTL)), now, now, now)
	return token, err
}

// SignIn checks the username and password and starts a session in the
// user's household. It returns ErrInvalidCredentials for an unknown user, a
// wrong password, or an account without an active membership.
func (s *Service) SignIn(ctx context.Context, username, password, userAgent string) (string, error) {
	var u struct {
		ID           uuid.UUID
		PasswordHash string
	}
	err := s.db.System().Read().Get(ctx, &u, `SELECT id, password_hash FROM users WHERE username_key = ?`, UsernameKey(username))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, _ = VerifyPassword(dummyHash, password)
		return "", ErrInvalidCredentials
	case err != nil:
		return "", err
	}
	ok, err := VerifyPassword(u.PasswordHash, password)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrInvalidCredentials
	}

	var token string
	err = s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		// The UI exposes one household; pin the oldest active membership.
		var householdID uuid.UUID
		err := q.Get(ctx, &householdID, `SELECT household_id FROM memberships
			WHERE user_id = ? AND removed_at IS NULL ORDER BY created_at LIMIT 1`, u.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidCredentials
		}
		if err != nil {
			return err
		}
		token, err = s.createSession(ctx, q, u.ID, householdID, userAgent)
		return err
	})
	return token, err
}

// sessionRow is the joined row Authenticate loads.
type sessionRow struct {
	ID                string
	UserID            uuid.UUID
	HouseholdID       uuid.UUID
	ExpiresAt         store.Time
	LastSeenAt        store.Time
	Role              Role
	RemovedAt         *store.Time
	Username          string
	Name              string
	Email             *string
	AvatarColor       string
	Theme             string
	Language          *string
	HouseholdName     string
	HouseholdLanguage string
	Timezone          string
	WeekStart         string
}

// Authenticate resolves a session token. When the session was last seen more
// than SessionTouchInterval ago its expiry slides forward and refreshed is
// true, so the caller can renew the cookie. It returns ErrNoSession for
// unknown or expired sessions and removed members.
func (s *Service) Authenticate(ctx context.Context, token string) (id *Identity, refreshed bool, err error) {
	if token == "" {
		return nil, false, ErrNoSession
	}
	var row sessionRow
	err = s.db.System().Read().Get(ctx, &row, `
		SELECT s.id, s.user_id, s.household_id, s.expires_at, s.last_seen_at,
		       m.role, m.removed_at,
		       u.username, u.name, u.email, u.avatar_color, u.theme, u.language,
		       h.name AS household_name, h.default_language AS household_language, h.timezone, h.week_start
		FROM sessions s
		JOIN memberships m ON m.household_id = s.household_id AND m.user_id = s.user_id
		JOIN users u ON u.id = s.user_id
		JOIN households h ON h.id = s.household_id
		WHERE s.id = ?`, hashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNoSession
	}
	if err != nil {
		return nil, false, err
	}
	now := s.now()
	if !now.Before(row.ExpiresAt.Time) || row.RemovedAt != nil {
		return nil, false, ErrNoSession
	}

	if now.Sub(row.LastSeenAt.Time) >= SessionTouchInterval {
		err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			at := store.T(now)
			_, err := q.Exec(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ?, updated_at = ? WHERE id = ?`,
				at, store.T(now.Add(SessionTTL)), at, row.ID)
			return err
		})
		if err != nil {
			return nil, false, err
		}
		refreshed = true
	}

	return &Identity{
		SessionID:         row.ID,
		UserID:            row.UserID,
		HouseholdID:       row.HouseholdID,
		Role:              row.Role,
		Username:          row.Username,
		Name:              row.Name,
		Email:             deref(row.Email),
		AvatarColor:       row.AvatarColor,
		Theme:             row.Theme,
		Language:          deref(row.Language),
		HouseholdName:     row.HouseholdName,
		HouseholdLanguage: row.HouseholdLanguage,
		Timezone:          row.Timezone,
		WeekStart:         row.WeekStart,
	}, refreshed, nil
}

// SignOut ends the session.
func (s *Service) SignOut(ctx context.Context, sessionID string) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID)
		return err
	})
}

// SessionInfo describes one signed-in device for the account page.
type SessionInfo struct {
	ID         string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	Current    bool
}

// Sessions lists the user's live sessions in the active household, the
// current one first, then by last use.
func (s *Service) Sessions(ctx context.Context, id *Identity) ([]SessionInfo, error) {
	var rows []struct {
		ID         string
		UserAgent  string
		CreatedAt  store.Time
		LastSeenAt store.Time
	}
	err := s.db.System().Read().Select(ctx, &rows, `SELECT id, user_agent, created_at, last_seen_at FROM sessions
		WHERE user_id = ? AND household_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`,
		id.UserID, id.HouseholdID, s.clock())
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(rows))
	for _, r := range rows {
		info := SessionInfo{ID: r.ID, UserAgent: r.UserAgent, CreatedAt: r.CreatedAt.Time, LastSeenAt: r.LastSeenAt.Time, Current: r.ID == id.SessionID}
		if info.Current {
			out = append([]SessionInfo{info}, out...)
		} else {
			out = append(out, info)
		}
	}
	return out, nil
}

// RevokeSession signs out one of the user's sessions. Revoking the current
// session is allowed and equals signing out. Unknown IDs, or sessions of
// other users, return ErrNotFound.
func (s *Service) RevokeSession(ctx context.Context, id *Identity, sessionID string) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, id.UserID)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// RevokeOtherSessions signs out all of the user's sessions except the
// current one.
func (s *Service) RevokeOtherSessions(ctx context.Context, id *Identity) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, id.UserID, id.SessionID)
		return err
	})
}
