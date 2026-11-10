package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/tenant"
)

// ErrShareInvalid means a public link's token is unknown or revoked, or
// its recipe is in the bin.
var ErrShareInvalid = errors.New("auth: share link invalid")

// maxShareTokenLen bounds tokens before they reach the database; issued
// tokens are 43 characters (internal/recipes).
const maxShareTokenLen = 64

// Share is what a public recipe link grants: reading one recipe of one
// household. The token is a bearer credential like a session cookie, so the
// auth layer resolves it; internal/recipes creates and revokes links.
type Share struct {
	HouseholdID uuid.UUID
	RecipeID    uuid.UUID
}

// ShareByToken resolves a public link. It looks the token up on every call,
// so a revoked link stops working at once. The token is the only input: the
// household and recipe come from the stored row, never from the request.
func (s *Service) ShareByToken(ctx context.Context, token string) (Share, error) {
	if token == "" || len(token) > maxShareTokenLen {
		return Share{}, ErrShareInvalid
	}
	var sh Share
	err := s.db.System().Read().Get(ctx, &sh, `
		SELECT sh.household_id, sh.recipe_id
		FROM recipe_shares sh
		JOIN recipes r ON r.household_id = sh.household_id AND r.id = sh.recipe_id
		WHERE sh.token = ? AND r.deleted_at IS NULL AND r.draft = ?`, token, false)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrShareInvalid
	}
	if err != nil {
		return Share{}, err
	}
	return sh, nil
}

type shareKey struct{}

// WithShare returns a context carrying a resolved public link and its
// household, so store.DB.Scope works for it. No user is set.
func WithShare(ctx context.Context, sh Share) context.Context {
	ctx = context.WithValue(ctx, shareKey{}, sh)
	return tenant.WithHousehold(ctx, sh.HouseholdID)
}

// ShareFromContext returns the public link resolved by RequireShare.
func ShareFromContext(ctx context.Context) (Share, bool) {
	sh, ok := ctx.Value(shareKey{}).(Share)
	return sh, ok && sh.HouseholdID != uuid.Nil
}

// RequireShare wraps the public link routes: it resolves the {token} path
// value and puts the link in the request context, or answers with notFound.
// Public routes run without a session (see web's skip list), so the only
// household in their context is the link's.
func (s *Service) RequireShare(notFound http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := FromContext(r.Context()); ok {
				// A session must never be combined with a link's household.
				panic("auth: RequireShare behind a loaded session")
			}
			sh, err := s.ShareByToken(r.Context(), r.PathValue("token"))
			switch {
			case errors.Is(err, ErrShareInvalid):
				notFound.ServeHTTP(w, r)
				return
			case err != nil:
				s.log.ErrorContext(r.Context(), "resolve share link", slog.Any("err", err))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithShare(r.Context(), sh)))
		})
	}
}
