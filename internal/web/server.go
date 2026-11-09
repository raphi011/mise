// Package web wires HTTP routes to domain packages and templ components.
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a-h/templ"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/live"
	"github.com/raphi011/mise/internal/mail"
	"github.com/raphi011/mise/internal/plan"
	"github.com/raphi011/mise/internal/recipes"
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

// Password-reset request limits: per client IP a burst of 5, then one a
// minute; per email address a burst of 3, then one every 10 minutes, so an
// inbox cannot be flooded from many addresses either.
const (
	resetIPBurst      = 5
	resetIPInterval   = time.Minute
	resetAddrBurst    = 3
	resetAddrInterval = 10 * time.Minute
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	cfg  config.Config
	db   *store.DB
	log  *slog.Logger
	csrf *http.CrossOriginProtection
	auth *auth.Service

	live      *live.Hub
	groceries *groceries.Service
	recipes   *recipes.Service
	plan      *plan.Service
	images    *images.Store
	// clock is the current time, for tests; nil means time.Now.
	clock func() time.Time
	// heartbeat overrides live.DefaultHeartbeat, for tests.
	heartbeat time.Duration

	ipLimit        *auth.Limiter
	pairLimit      *auth.Limiter
	userLimit      *auth.Limiter
	passwordLimit  *auth.Limiter
	resetIPLimit   *auth.Limiter
	resetAddrLimit *auth.Limiter

	// mailer sends invite and reset emails; nil without SMTP.
	mailer mail.Mailer
	// bg tracks work that outlives its request (reset emails).
	bg sync.WaitGroup

	// baseURL is the public URL: MISE_BASE_URL, or the one stored by setup.
	baseURL atomic.Pointer[url.URL]
}

// New returns a Server. db must have been migrated with db.Migrate; until
// then /readyz reports not ready. Call LoadSettings before serving so the
// base URL stored by first-run setup is known.
func New(cfg config.Config, db *store.DB, log *slog.Logger) (*Server, error) {
	s := &Server{
		cfg:            cfg,
		db:             db,
		log:            log,
		csrf:           http.NewCrossOriginProtection(),
		ipLimit:        auth.NewLimiter(ipBurst, ipInterval, limiterMaxKeys, nil),
		pairLimit:      auth.NewLimiter(pairBurst, pairInterval, limiterMaxKeys, nil),
		userLimit:      auth.NewLimiter(userBurst, userInterval, limiterMaxKeys, nil),
		passwordLimit:  auth.NewLimiter(passwordBurst, passwordInterval, limiterMaxKeys, nil),
		resetIPLimit:   auth.NewLimiter(resetIPBurst, resetIPInterval, limiterMaxKeys, nil),
		resetAddrLimit: auth.NewLimiter(resetAddrBurst, resetAddrInterval, limiterMaxKeys, nil),
	}
	if cfg.SMTP.Enabled() {
		s.mailer = mail.NewSMTP(cfg.SMTP)
	}
	s.live = live.NewHub()
	s.groceries = groceries.New(s.live, nil)
	s.recipes = recipes.New(s.live, s.groceries, nil)
	s.plan = plan.New(s.live, s.recipes, s.now)
	s.images = images.New(cfg.DataDir)
	s.auth = auth.New(db, auth.Config{Log: log, SecureCookie: s.secureCookie, OnSetup: s.seedHousehold})
	if cfg.BaseURL != nil {
		if err := s.setBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// now returns the current time.
func (s *Server) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// Wait blocks until background work started by requests (reset emails)
// is done. Call it after the HTTP server has shut down.
func (s *Server) Wait() { s.bg.Wait() }

// Images returns the image store, for the sweep of unreferenced images.
func (s *Server) Images() *images.Store { return s.images }

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
	switch path {
	case "/healthz", "/readyz", "/favicon.ico", "/manifest.webmanifest", "/sw.js":
		return true
	}
	return strings.HasPrefix(path, ui.StaticPrefix)
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
	r.read("/manifest.webmanifest", http.HandlerFunc(manifest))
	r.read("/sw.js", http.HandlerFunc(serviceWorker))

	// Signed out.
	r.read("/setup", http.HandlerFunc(s.setupPage))
	r.write(http.MethodPost, "/setup", http.HandlerFunc(s.setupSubmit))
	r.read("/signin", http.HandlerFunc(s.signInPage))
	r.write(http.MethodPost, "/signin", http.HandlerFunc(s.signInSubmit))
	r.read("/forgot-password", http.HandlerFunc(s.forgotPasswordPage))
	r.write(http.MethodPost, "/forgot-password", http.HandlerFunc(s.forgotPasswordSubmit))
	r.read("/reset/{token}", http.HandlerFunc(s.resetPage))
	r.write(http.MethodPost, "/reset/{token}", http.HandlerFunc(s.resetSubmit))
	r.read("/invite/{token}", http.HandlerFunc(s.acceptInvitePage))
	r.write(http.MethodPost, "/invite/{token}", http.HandlerFunc(s.acceptInviteSubmit))
	r.write(http.MethodPost, "/signout", s.user(s.signOut))

	// App.
	r.read("/{$}", s.user(http.RedirectHandler("/groceries", http.StatusFound).ServeHTTP))
	r.read("/recipes", s.user(s.libraryPage))
	r.read("/recipes/filters", s.user(s.filterPage))
	r.read("/recipes/filters/count", s.user(s.filterCount))
	r.read("/recipes/search", s.user(s.searchPage))
	r.read("/recipes/search/results", s.user(s.searchResults))
	r.read("/recipes/new", s.user(s.newRecipePage))
	r.write(http.MethodPost, "/recipes", s.user(s.createRecipe))
	r.read("/recipes/editor/ingredient", s.user(s.ingredientRow))
	r.read("/recipes/{id}", s.user(s.recipePage))
	r.read("/recipes/{id}/edit", s.user(s.editRecipePage))
	r.write(http.MethodPost, "/recipes/{id}/edit", s.user(s.updateRecipe))
	r.read("/recipes/{id}/menu", s.user(s.recipeMenu))
	r.read("/recipes/{id}/cook", s.user(s.cookPage))
	r.write(http.MethodPost, "/recipes/{id}/favourite", s.user(s.setFavourite))
	r.write(http.MethodPost, "/recipes/{id}/ingredients/{ing}/toggle", s.user(s.toggleIngredient))
	r.write(http.MethodPost, "/recipes/{id}/ingredients/restore", s.user(s.restoreIngredients))
	r.read("/recipes/{id}/delete", s.user(s.deleteRecipePage))
	r.write(http.MethodPost, "/recipes/{id}/delete", s.user(s.deleteRecipe))
	r.write(http.MethodPost, "/recipes/{id}/restore", s.user(s.restoreRecipe))
	r.write(http.MethodPost, "/recipes/{id}/cooked", s.user(s.markCooked))
	r.write(http.MethodPost, "/recipes/{id}/cooked/{event}/undo", s.user(s.undoCooked))
	r.read("/images/{hash}/{size}", s.user(s.image))
	r.read("/plan", s.user(s.planPage))
	r.read("/plan/month", s.user(s.planMonthPage))
	r.write(http.MethodPost, "/plan", s.user(s.addMeal))
	r.read("/plan/add", s.user(s.addPlanPage))
	r.read("/plan/add/days", s.user(s.addPlanDays))
	r.read("/plan/pick", s.user(s.pickPage))
	r.read("/plan/pick/results", s.user(s.pickResults))
	r.write(http.MethodPost, "/plan/place", s.user(s.placeMeals))
	r.read("/plan/meals/{id}", s.user(s.mealMenu))
	r.write(http.MethodPost, "/plan/meals/{id}/move", s.user(s.moveMeal))
	r.write(http.MethodPost, "/plan/meals/{id}/servings", s.user(s.setMealServings))
	r.write(http.MethodPost, "/plan/meals/{id}/replace", s.user(s.replaceMeal))
	r.write(http.MethodPost, "/plan/meals/{id}/remove", s.user(s.removeMeal))
	r.write(http.MethodPost, "/plan/meals/{id}/restore", s.user(s.restoreMeal))
	r.write(http.MethodPost, "/plan/meals/{id}/unplan", s.user(s.unplanMeal))
	r.read("/groceries", s.user(s.groceriesHome))
	r.read("/groceries/lists/{id}", s.user(s.listPage))
	r.read("/groceries/lists/{id}/suggest", s.user(s.suggest))
	r.write(http.MethodPost, "/groceries/lists/{id}/entries", s.user(s.addEntry))
	r.read("/groceries/lists/{id}/clear", s.user(s.clearCartPage))
	r.write(http.MethodPost, "/groceries/lists/{id}/clear", s.user(s.clearCart))
	r.write(http.MethodPost, "/groceries/lists/{id}/restore-cart", s.user(s.restoreCart))
	r.write(http.MethodPost, "/groceries/entries/{id}/check", s.user(s.checkEntry))
	r.write(http.MethodPost, "/groceries/entries/{id}/uncheck", s.user(s.uncheckEntry))
	r.read("/groceries/entries/{id}/edit", s.user(s.editEntryPage))
	r.write(http.MethodPost, "/groceries/entries/{id}/edit", s.user(s.saveEntry))
	r.write(http.MethodPost, "/groceries/entries/{id}/remove", s.user(s.removeEntry))
	r.write(http.MethodPost, "/groceries/entries/{id}/restore", s.user(s.restoreEntry))
	r.read("/live", s.user(s.liveStream))

	// Settings.
	r.read("/settings", s.user(s.settingsPage))
	r.write(http.MethodPost, "/settings/preferences", s.user(s.savePreferences))
	r.read("/settings/account", s.user(s.accountPage))
	r.write(http.MethodPost, "/settings/account/profile", s.user(s.saveProfile))
	r.write(http.MethodPost, "/settings/account/password", s.user(s.changePassword))
	r.write(http.MethodPost, "/settings/account/sessions/revoke-others", s.user(s.revokeOtherSessions))
	r.write(http.MethodPost, "/settings/account/sessions/{id}/revoke", s.user(s.revokeSession))
	r.write(http.MethodPost, "/settings/lists", s.user(s.createList))
	r.write(http.MethodPost, "/settings/lists/{id}/rename", s.user(s.renameList))
	r.write(http.MethodPost, "/settings/lists/{id}/move", s.user(s.moveList))
	r.write(http.MethodPost, "/settings/lists/{id}/delete", s.user(s.deleteList))
	r.write(http.MethodPost, "/settings/lists/{id}/restore", s.user(s.restoreList))
	r.read("/settings/categories", s.user(s.categoriesPage))
	r.write(http.MethodPost, "/settings/categories", s.user(s.createCategory))
	r.write(http.MethodPost, "/settings/categories/{id}", s.user(s.updateCategory))
	r.write(http.MethodPost, "/settings/categories/{id}/move", s.user(s.moveCategory))
	r.write(http.MethodPost, "/settings/categories/{id}/delete", s.user(s.deleteCategory))
	r.read("/settings/tags", s.user(s.tagsPage))
	r.write(http.MethodPost, "/settings/tags", s.user(s.createTag))
	r.write(http.MethodPost, "/settings/tags/{id}/rename", s.user(s.renameTag))
	r.write(http.MethodPost, "/settings/tags/{id}/pin", s.user(s.pinTag))
	r.write(http.MethodPost, "/settings/tags/{id}/merge", s.user(s.mergeTag))
	r.write(http.MethodPost, "/settings/tags/{id}/delete", s.user(s.deleteTag))
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
