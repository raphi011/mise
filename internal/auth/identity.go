package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/store"
)

// Single sign-on errors. SSO never creates accounts: an identity signs in
// only as the account it is linked to, and it is linked only from a
// signed-in session (Link). Linking by email address is not done: the
// account's address is whatever its owner typed, so a member could claim
// a housemate's provider identity.
var (
	// ErrNoAccount means no active account is linked to the identity.
	ErrNoAccount = errors.New("auth: no account linked to this identity")
	// ErrIdentityLinkedElsewhere means the identity is already linked to
	// another account.
	ErrIdentityLinkedElsewhere = errors.New("auth: identity linked to another account")
	// ErrOtherIdentityLinked means the account is already linked to another
	// identity of the same provider.
	ErrOtherIdentityLinked = errors.New("auth: account linked to another identity")
	// ErrNoPassword means unlinking would leave the account without a way
	// to sign in.
	ErrNoPassword = errors.New("auth: account has no password")
)

// ExternalIdentity is a verified OpenID Connect ID token's identity.
type ExternalIdentity struct {
	Issuer  string
	Subject string
}

// SignInExternal starts a session for the account linked to ext. It
// returns ErrNoAccount when no active account is linked.
func (s *Service) SignInExternal(ctx context.Context, ext ExternalIdentity, userAgent string) (string, error) {
	if ext.Issuer == "" || ext.Subject == "" {
		return "", ErrNoAccount
	}
	var token string
	err := s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var userID uuid.UUID
		err := q.Get(ctx, &userID, `SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?`, ext.Issuer, ext.Subject)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoAccount
		}
		if err != nil {
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

// Link links ext to the signed-in user, who has just signed in at the
// provider as ext. Linking it again is a no-op. It returns
// ErrIdentityLinkedElsewhere when ext belongs to another account and
// ErrOtherIdentityLinked when the user already has another identity of
// the issuer.
func (s *Service) Link(ctx context.Context, id *Identity, ext ExternalIdentity) error {
	if ext.Issuer == "" || ext.Subject == "" {
		return errors.New("auth: link an identity without issuer or subject")
	}
	return s.db.System().Tx(ctx, func(ctx context.Context, q store.Querier) error {
		var owner uuid.UUID
		err := q.Get(ctx, &owner, `SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ?`, ext.Issuer, ext.Subject)
		switch {
		case err == nil && owner == id.UserID:
			return nil
		case err == nil:
			return ErrIdentityLinkedElsewhere
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM user_identities WHERE user_id = ? AND issuer = ?`, id.UserID, ext.Issuer).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrOtherIdentityLinked
		}
		_, err = q.Exec(ctx, `INSERT INTO user_identities (issuer, subject, user_id, created_at) VALUES (?, ?, ?, ?)`,
			ext.Issuer, ext.Subject, id.UserID, s.clock())
		if store.IsUniqueViolation(err) {
			// A concurrent link of the same identity or account won.
			return ErrIdentityLinkedElsewhere
		}
		return err
	})
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
