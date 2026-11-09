package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Single sign-on errors. SSO never creates accounts: an identity signs in
// only as an account it is linked to, or links to the one active account
// with its verified email address.
var (
	// ErrNoAccount means no active account is linked to the identity or uses
	// its email address.
	ErrNoAccount = errors.New("auth: no account for this identity")
	// ErrEmailUnverified means the provider does not vouch for the email
	// address, so it cannot be used to link an account.
	ErrEmailUnverified = errors.New("auth: email address not verified by the provider")
	// ErrAmbiguousEmail means several active accounts use the email address.
	ErrAmbiguousEmail = errors.New("auth: several accounts use this email address")
	// ErrOtherIdentityLinked means the account matching the email address is
	// already linked to another identity of the same provider.
	ErrOtherIdentityLinked = errors.New("auth: account linked to another identity")
	// ErrNoPassword means unlinking would leave the account without a way
	// to sign in.
	ErrNoPassword = errors.New("auth: account has no password")
)

// ExternalIdentity is a verified OpenID Connect ID token's identity.
type ExternalIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
}

// SignInExternal starts a session for the account linked to ext. An
// identity not linked yet is linked first to the one active account whose
// email address (compared case-insensitively) is ext's verified address.
func (s *Service) SignInExternal(ctx context.Context, ext ExternalIdentity, userAgent string) (string, error) {
	if ext.Issuer == "" || ext.Subject == "" {
		return "", ErrNoAccount
	}
	var token string
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var userID uuid.UUID
		err := q.Get(ctx, &userID, `SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?`, ext.Issuer, ext.Subject)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if userID, err = s.linkByEmail(ctx, q, ext); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		token, err = s.signInUser(ctx, q, userID, userAgent)
		if errors.Is(err, ErrInvalidCredentials) {
			return ErrNoAccount
		}
		return err
	})
	return token, err
}

// linkByEmail links ext to the active account with its email address.
func (s *Service) linkByEmail(ctx context.Context, q store.Querier, ext ExternalIdentity) (uuid.UUID, error) {
	email := strings.TrimSpace(ext.Email)
	if email == "" || len(email) > maxEmailLen {
		return uuid.Nil, ErrNoAccount
	}
	if !ext.EmailVerified {
		return uuid.Nil, ErrEmailUnverified
	}
	// Compared in Go like RequestPasswordReset, so both engines fold alike.
	var rows []struct {
		ID    uuid.UUID
		Email string
	}
	if err := q.Select(ctx, &rows, `
		SELECT DISTINCT u.id, u.email
		FROM users u
		JOIN memberships m ON m.user_id = u.id AND m.removed_at IS NULL
		WHERE u.email IS NOT NULL AND u.email <> ''`); err != nil {
		return uuid.Nil, err
	}
	var match []uuid.UUID
	for _, r := range rows {
		if strings.EqualFold(r.Email, email) {
			match = append(match, r.ID)
		}
	}
	switch len(match) {
	case 0:
		return uuid.Nil, ErrNoAccount
	case 1:
	default:
		return uuid.Nil, ErrAmbiguousEmail
	}
	_, err := q.Exec(ctx, `INSERT INTO user_identities (issuer, subject, user_id, created_at) VALUES (?, ?, ?, ?)`,
		ext.Issuer, ext.Subject, match[0], s.clock())
	if store.IsUniqueViolation(err) {
		// (user_id, issuer) is taken; a concurrent first sign-in of the same
		// identity ends up here too and can simply retry.
		return uuid.Nil, ErrOtherIdentityLinked
	}
	return match[0], err
}

// LinkedIdentity reports whether the user has an identity of the issuer
// linked.
func (s *Service) LinkedIdentity(ctx context.Context, userID uuid.UUID, issuer string) (bool, error) {
	var n int
	err := s.db.System().Read().QueryRow(ctx, `SELECT count(*) FROM user_identities WHERE user_id = ? AND issuer = ?`,
		userID, issuer).Scan(&n)
	return n > 0, err
}

// Unlink removes the signed-in user's identity of the issuer. It returns
// ErrNoPassword when the account has no password to sign in with instead,
// and ErrNotFound when nothing is linked.
func (s *Service) Unlink(ctx context.Context, id *Identity, issuer string) error {
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var hash string
		if err := q.Get(ctx, &hash, `SELECT password_hash FROM users WHERE id = ?`, id.UserID); err != nil {
			return err
		}
		if hash == "" {
			return ErrNoPassword
		}
		res, err := q.Exec(ctx, `DELETE FROM user_identities WHERE user_id = ? AND issuer = ?`, id.UserID, issuer)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}
