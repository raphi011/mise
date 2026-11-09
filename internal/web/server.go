// Package web wires HTTP routes to domain packages and templ components.
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/a-h/templ"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// Sign-in rate limits. Every attempt spends from three buckets:
//   - per client IP: a burst of 10, then one every 6 s;
//   - per client IP and username: a burst of 5, then one a minute;
//   - per username, from any IP: a burst of 20, then one every 15 s. One IP
//     is held to the slower pair rate, so it cannot keep this bucket empty;
//     locking someone out takes attempts from several addresses.
//
// A successful sign-in gives its attempts back. The current-password check
// on the account page has its own bucket per signed-in user, so failed
// sign-ins by others cannot block a password change.
const (
	ipBurst          = 10
	ipInterval       = 6 * time.Second
	pairBurst        = 5
	pairInterval     = time.Minute
	userBurst        = 20
	userInterval     = 15 * time.Second
	passwordBurst    = 5
	passwordInterval = 30 * time.Second
	limiterMaxKeys   = 10_000
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	cfg  config.Config
	db   *store.DB
	log  *slog.Logger
	csrf *http.CrossOriginProtection
	auth *auth.Service

	ipLimit       *auth.Limiter
	pairLimit     *auth.Limiter
	userLimit     *auth.Limiter
	passwordLimit *auth.Limiter

	// baseURL is the public URL: MISE_BASE_URL, or the one stored by setup.
	baseURL atomic.Pointer[url.URL]
}

// New returns a Server. db must have been migrated with db.Migrate; until
// then /readyz reports not ready. Call LoadSettings before serving so the
// base URL stored by first-run setup is known.
func New(cfg config.Config, db *store.DB, log *slog.Logger) (*Server, error) {
	s := &Server{
		cfg:           cfg,
		db:            db,
		log:           log,
		csrf:          http.NewCrossOriginProtection(),
		ipLimit:       auth.NewLimiter(ipBurst, ipInterval, limiterMaxKeys, nil),
		pairLimit:     auth.NewLimiter(pairBurst, pairInterval, limiterMaxKeys, nil),
		userLimit:     auth.NewLimiter(userBurst, userInterval, limiterMaxKeys, nil),
		passwordLimit: auth.NewLimiter(passwordBurst, passwordInterval, limiterMaxKeys, nil),
	}
	s.auth = auth.New(db, auth.Config{Log: log, SecureCookie: s.secureCookie})
	if cfg.BaseURL != nil {
		if err := s.setBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// LoadSettings reads server-wide settings saved by first-run setup. When
// MISE_BASE_URL is unset, the stored base URL becomes the public URL and a
// trusted origin for cross-origin protection.
func (s *Server) LoadSettings(ctx context.Context) error {
	if s.cfg.BaseURL != nil {
		return nil
	}
	u, err := s.auth.StoredBaseURL(ctx)
	if err != nil || u == nil {
		return err
	}
	return s.setBaseURL(u)
}

func (s *Server) setBaseURL(u *url.URL) error {
	if err := s.AddTrustedOrigin(u.Scheme + "://" + u.Host); err != nil {
		return err
	}
	s.baseURL.Store(u)
	return nil
}

// BaseURL returns the public base URL, or nil before setup when
// MISE_BASE_URL is unset.
func (s *Server) BaseURL() *url.URL { return s.baseURL.Load() }

// secureCookie reports whether cookies need the Secure attribute.
func (s *Server) secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	u := s.baseURL.Load()
	return u != nil && u.Scheme == "https"
}

// AddTrustedOrigin lets cross-origin writes through when their Origin header
// is exactly origin ("scheme://host[:port]"), e.g. the public base URL behind
// a proxy that rewrites Host. It is safe to call while serving and applies
// to later requests, so first-run setup can add the base URL it stores.
func (s *Server) AddTrustedOrigin(origin string) error {
	if err := s.csrf.AddTrustedOrigin(origin); err != nil {
		return fmt.Errorf("web: trusted origin %q: %w", origin, err)
	}
	return nil
}

// Handler returns the root handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	r := newRouter()
	s.routes(r)

	var h http.Handler = r
	h = withLocale(h)
	h = s.requireSetup(h)
	h = skipForInfra(s.auth.LoadSession, h)
	h = s.csrf.Handler(h)
	h = securityHeaders(h)
	h = recoverPanics(s.log, h)
	h = logRequests(s.log, s.cfg.TrustedProxies, r.pattern, h)
	return h
}

// isInfra reports whether a path serves assets or probes, which need no
// session, locale or setup.
func isInfra(path string) bool {
	return strings.HasPrefix(path, ui.StaticPrefix) || path == "/healthz" || path == "/readyz" || path == "/favicon.ico"
}

// skipForInfra applies mw to every request except infrastructure paths, so
// asset and probe requests don't query the session table.
func skipForInfra(mw func(http.Handler) http.Handler, next http.Handler) http.Handler {
	wrapped := mw(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isInfra(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		wrapped.ServeHTTP(w, r)
	})
}

// requireSetup sends every page to /setup until a household exists.
func (s *Server) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isInfra(r.URL.Path) || r.URL.Path == "/setup" {
			next.ServeHTTP(w, r)
			return
		}
		need, err := s.auth.NeedsSetup(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if need {
			status := http.StatusFound
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				status = http.StatusSeeOther
			}
			http.Redirect(w, r, "/setup", status)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// user and admin wrap handlers that need a signed-in user or an admin.
func (s *Server) user(h http.HandlerFunc) http.Handler { return auth.RequireUser(h) }

func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return auth.RequireAdmin(http.HandlerFunc(s.forbidden))(h)
}

func (s *Server) routes(r *router) {
	r.read("/healthz", http.HandlerFunc(s.healthz))
	r.read("/readyz", http.HandlerFunc(s.readyz))
	r.read(ui.StaticPrefix, ui.StaticHandler())
	// Browsers request /favicon.ico regardless of <link rel="icon">. The
	// target is content-hashed, so the redirect must not be cached forever.
	r.read("/favicon.ico", http.RedirectHandler(ui.Asset("img/icon.svg"), http.StatusFound))

	// Signed out.
	r.read("/setup", http.HandlerFunc(s.setupPage))
	r.write(http.MethodPost, "/setup", http.HandlerFunc(s.setupSubmit))
	r.read("/signin", http.HandlerFunc(s.signInPage))
	r.write(http.MethodPost, "/signin", http.HandlerFunc(s.signInSubmit))
	r.read("/forgot-password", http.HandlerFunc(s.forgotPasswordPage))
	r.read("/invite/{token}", http.HandlerFunc(s.acceptInvitePage))
	r.write(http.MethodPost, "/invite/{token}", http.HandlerFunc(s.acceptInviteSubmit))
	r.write(http.MethodPost, "/signout", s.user(s.signOut))

	// App.
	r.read("/{$}", s.user(http.RedirectHandler("/groceries", http.StatusFound).ServeHTTP))
	r.read("/recipes", s.user(s.placeholder(ui.NavRecipes, "page.recipes.title")))
	r.read("/plan", s.user(s.placeholder(ui.NavPlan, "page.plan.title")))
	r.read("/groceries", s.user(s.placeholder(ui.NavGroceries, "page.groceries.title")))

	// Settings.
	r.read("/settings", s.user(s.settingsPage))
	r.write(http.MethodPost, "/settings/preferences", s.user(s.savePreferences))
	r.read("/settings/account", s.user(s.accountPage))
	r.write(http.MethodPost, "/settings/account/profile", s.user(s.saveProfile))
	r.write(http.MethodPost, "/settings/account/password", s.user(s.changePassword))
	r.write(http.MethodPost, "/settings/account/sessions/revoke-others", s.user(s.revokeOtherSessions))
	r.write(http.MethodPost, "/settings/account/sessions/{id}/revoke", s.user(s.revokeSession))
	r.read("/settings/invite", s.admin(s.invitePage))
	r.write(http.MethodPost, "/settings/invites", s.admin(s.createInvite))
	r.write(http.MethodPost, "/settings/invites/{id}/revoke", s.admin(s.revokeInvite))
	r.read("/settings/people/{id}", s.admin(s.memberPage))
	r.write(http.MethodPost, "/settings/people/{id}/role", s.admin(s.setRole))
	r.write(http.MethodPost, "/settings/people/{id}/password", s.admin(s.resetPassword))
	r.write(http.MethodPost, "/settings/people/{id}/remove", s.admin(s.removeMember))

	r.fallback(http.HandlerFunc(s.notFound))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

// readyz reports whether the database is migrated and reachable. It only
// uses the read pool, so it stays responsive while SQLite's single writer
// connection is busy.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.db.Migrated() {
		s.log.WarnContext(ctx, "readyz: migrations not applied")
		http.Error(w, "migrations pending", http.StatusServiceUnavailable)
		return
	}
	if err := s.db.Ping(ctx); err != nil {
		s.log.WarnContext(ctx, "readyz: database unreachable", slog.Any("err", err))
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

// page returns the document settings for a page: title, active nav entry,
// and the signed-in user's theme and avatar.
func (s *Server) page(r *http.Request, title string, nav ui.NavItem) ui.Page {
	p := ui.Page{Title: title, Active: nav}
	if id, ok := auth.FromContext(r.Context()); ok {
		p.User = &ui.Viewer{Name: id.Name, Color: id.AvatarColor, Admin: id.IsAdmin()}
		if id.Theme != "system" {
			p.Theme = id.Theme
		}
	}
	return p
}

func (s *Server) placeholder(nav ui.NavItem, titleKey i18n.Key) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, http.StatusOK, ui.Placeholder(s.page(r, i18n.T(r.Context(), titleKey), nav)))
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := s.page(r, i18n.T(ctx, "error.notFound.title"), ui.NavNone)
	s.render(w, r, http.StatusNotFound, ui.ErrorPage(p, i18n.T(ctx, "error.notFound.body")))
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := s.page(r, i18n.T(ctx, "error.forbidden.title"), ui.NavNone)
	s.render(w, r, http.StatusForbidden, ui.ErrorPage(p, i18n.T(ctx, "error.forbidden.body")))
}

// serverError logs err and answers 500.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "request failed", slog.String("route", r.Pattern), slog.Any("err", err))
	http.Error(w, i18n.T(r.Context(), "error.internal.title"), http.StatusInternalServerError)
}

// render writes a templ component with the given status. Rendering goes to a
// buffer first so a failed render becomes a clean 500.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templ.Handler(c,
		templ.WithStatus(status),
		templ.WithErrorHandler(func(r *http.Request, err error) http.Handler {
			s.log.ErrorContext(r.Context(), "render", slog.String("route", r.Pattern), slog.Any("err", err))
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, i18n.T(r.Context(), "error.internal.title"), http.StatusInternalServerError)
			})
		}),
	).ServeHTTP(w, r)
}

// seeOther redirects after a successful POST (post/redirect/get).
func seeOther(w http.ResponseWriter, r *http.Request, target string) {
	http.Redirect(w, r, target, http.StatusSeeOther)
}
