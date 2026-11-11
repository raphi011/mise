package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// API tokens are bearer credentials for scripts and agents. A token acts as
// its member in the household it was created in, never with more than its
// scope; only a signed-in browser session creates or revokes them.

// TokenPrefix starts every API token, so secret scanners and people can
// recognise one.
const TokenPrefix = "mise_"

// TokenScope is what a token may do.
type TokenScope string

// Token scopes.
const (
	// ScopeRead reads recipes, the plan and the lists.
	ScopeRead TokenScope = "read"
	// ScopeWrite also changes them.
	ScopeWrite TokenScope = "write"
)

// Valid reports whether s is a known scope.
func (s TokenScope) Valid() bool { return s == ScopeRead || s == ScopeWrite }

// Can reports whether the scope allows writing when write is set, and
// reading otherwise.
func (s TokenScope) Can(write bool) bool { return s == ScopeWrite || (s == ScopeRead && !write) }

// Token limits.
const (
	maxTokenNameLen  = 60
	maxTokensPerUser = 20
	// tokenTouchInterval limits how often last_used_at is written.
	tokenTouchInterval = time.Hour
)

// Token errors.
var (
	// ErrNoToken means the bearer token is unknown, expired, or its member
	// was removed.
	ErrNoToken = errors.New("auth: no valid API token")
	// ErrTooManyTokens means the user is at the token limit.
	ErrTooManyTokens = errors.New("auth: too many API tokens")
)

// TokenInfo describes one token for the account page.
type TokenInfo struct {
	ID         uuid.UUID
	Name       string
	Scope      TokenScope
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// NewTokenInput is a token to create.
type NewTokenInput struct {
	Name  string
	Scope TokenScope
	// TTL is how long the token lives; zero means until revoked.
	TTL time.Duration
}

// CreateToken makes a token for the signed-in user in their household and
// returns it. The token is shown once: only its hash is stored. Invalid
// input returns FieldErrors keyed by "name" and "scope".
func (s *Service) CreateToken(ctx context.Context, id *Identity, in NewTokenInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	fe := FieldErrors{}
	switch {
	case name == "":
		fe["name"] = "form.error.required"
	case len([]rune(name)) > maxTokenNameLen:
		fe["name"] = "form.error.tooLong"
	}
	if !in.Scope.Valid() {
		fe["scope"] = "form.error.choice"
	}
	if err := fe.orNil(); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails; see crypto/rand.Read
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	now := s.clock()
	var expires *store.Time
	if in.TTL > 0 {
		t := store.T(s.now().Add(in.TTL))
		expires = &t
	}
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var n int
		if err := q.Get(ctx, &n, `SELECT count(*) FROM api_tokens WHERE household_id = ? AND user_id = ?`, id.HouseholdID, id.UserID); err != nil {
			return err
		}
		if n >= maxTokensPerUser {
			return ErrTooManyTokens
		}
		_, err := q.Exec(ctx, `INSERT INTO api_tokens (id, household_id, user_id, name, token_hash, scope, expires_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			store.NewID(), id.HouseholdID, id.UserID, name, hashToken(token), string(in.Scope), expires, now, now)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// Tokens lists the user's tokens in their household, newest first.
func (s *Service) Tokens(ctx context.Context, id *Identity) ([]TokenInfo, error) {
	var rows []struct {
		ID         uuid.UUID
		Name       string
		Scope      TokenScope
		CreatedAt  store.Time
		LastUsedAt *store.Time
		ExpiresAt  *store.Time
	}
	err := s.db.System().Read().Select(ctx, &rows, `SELECT id, name, scope, created_at, last_used_at, expires_at FROM api_tokens
		WHERE household_id = ? AND user_id = ? ORDER BY created_at DESC, id`, id.HouseholdID, id.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]TokenInfo, 0, len(rows))
	for _, r := range rows {
		t := TokenInfo{ID: r.ID, Name: r.Name, Scope: r.Scope, CreatedAt: r.CreatedAt.Time}
		if r.LastUsedAt != nil {
			v := r.LastUsedAt.Time
			t.LastUsedAt = &v
		}
		if r.ExpiresAt != nil {
			v := r.ExpiresAt.Time
			t.ExpiresAt = &v
		}
		out = append(out, t)
	}
	return out, nil
}

// RevokeToken deletes one of the user's tokens. Unknown IDs and other
// users' tokens return ErrNotFound.
func (s *Service) RevokeToken(ctx context.Context, id *Identity, tokenID uuid.UUID) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		res, err := q.Exec(ctx, `DELETE FROM api_tokens WHERE id = ? AND household_id = ? AND user_id = ?`,
			tokenID, id.HouseholdID, id.UserID)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// AuthenticateToken resolves a bearer token to its member's identity in the
// token's household, with the token's scope in Identity.APIScope. It returns
// ErrNoToken for unknown or expired tokens and removed members.
func (s *Service) AuthenticateToken(ctx context.Context, token string) (*Identity, error) {
	if !strings.HasPrefix(token, TokenPrefix) || len(token) > 200 {
		return nil, ErrNoToken
	}
	var row struct {
		sessionRow
		TokenID    uuid.UUID
		Scope      TokenScope
		TokenExp   *store.Time
		LastUsedAt *store.Time
	}
	err := s.db.System().Read().Get(ctx, &row, `
		SELECT t.id AS token_id, t.user_id, t.household_id, t.scope, t.expires_at AS token_exp, t.last_used_at,
		       m.role, m.removed_at,
		       u.username, u.name, u.email, u.avatar_color, u.theme, u.language, u.units,
		       h.name AS household_name, h.default_language AS household_language, h.timezone, h.week_start
		FROM api_tokens t
		JOIN memberships m ON m.household_id = t.household_id AND m.user_id = t.user_id
		JOIN users u ON u.id = t.user_id
		JOIN households h ON h.id = t.household_id
		WHERE t.token_hash = ?`, hashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if row.RemovedAt != nil || (row.TokenExp != nil && !now.Before(row.TokenExp.Time)) {
		return nil, ErrNoToken
	}
	if row.LastUsedAt == nil || now.Sub(row.LastUsedAt.Time) >= tokenTouchInterval {
		err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
			_, err := q.Exec(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, store.T(now), row.TokenID)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	id := row.identity()
	id.TokenID = row.TokenID
	id.APIScope = row.Scope
	return id, nil
}
