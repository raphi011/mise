package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/raphi011/mise/internal/tenant"
)

// CookieName is the session cookie.
const CookieName = "mise_session"

type identityKey struct{}

// WithIdentity returns a context carrying the signed-in identity, its
// household and user (so store.DB.Scope works).
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	ctx = context.WithValue(ctx, identityKey{}, id)
	ctx = tenant.WithHousehold(ctx, id.HouseholdID)
	return tenant.WithUser(ctx, id.UserID)
}

// FromContext returns the signed-in identity, if any.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// SetSessionCookie writes the session cookie for token.
func (s *Service) SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the session cookie.
func (s *Service) ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// LoadSession resolves the session cookie, if any, and puts the identity in
// the request context. Requests without a valid session pass through
// unauthenticated (a stale cookie is cleared); RequireUser rejects them where
// needed.
func (s *Service) LoadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		id, refreshed, err := s.Authenticate(r.Context(), c.Value)
		switch {
		case errors.Is(err, ErrNoSession):
			s.ClearSessionCookie(w, r)
			next.ServeHTTP(w, r)
			return
		case err != nil:
			s.log.ErrorContext(r.Context(), "load session", slog.Any("err", err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if refreshed {
			s.SetSessionCookie(w, r, c.Value)
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// RequireUser rejects requests without a signed-in user: page requests are
// redirected to /signin (returning to the page afterwards), Datastar
// requests get 401.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Datastar-Request") != "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		target := "/signin"
		if r.Method == http.MethodGet && r.URL.Path != "/" {
			target += "?next=" + url.QueryEscape(r.URL.RequestURI())
		}
		status := http.StatusFound
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			status = http.StatusSeeOther
		}
		http.Redirect(w, r, target, status)
	})
}

// RequireAdmin wraps handlers that only admins may use; others get
// forbidden. It implies RequireUser.
func RequireAdmin(forbidden http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, _ := FromContext(r.Context()); !id.IsAdmin() {
				forbidden.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// SafeNext returns next if it is a local path to return to after signing
// in, or "/" otherwise. It rejects absolute and scheme-relative URLs.
func SafeNext(next string) string {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.ContainsAny(next, "\r\n") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return next
}
