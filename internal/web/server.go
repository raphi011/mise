// Package web wires HTTP routes to domain packages and templ components.
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"

	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/ui"
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	cfg  config.Config
	db   *store.DB
	log  *slog.Logger
	csrf *http.CrossOriginProtection
}

// New returns a Server. db must have been migrated with db.Migrate; until
// then /readyz reports not ready.
func New(cfg config.Config, db *store.DB, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, db: db, log: log, csrf: http.NewCrossOriginProtection()}
	if cfg.BaseURL != nil {
		if err := s.AddTrustedOrigin(cfg.BaseURL.Scheme + "://" + cfg.BaseURL.Host); err != nil {
			return nil, err
		}
	}
	return s, nil
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
	h = s.csrf.Handler(h)
	h = securityHeaders(h)
	h = recoverPanics(s.log, h)
	h = logRequests(s.log, s.cfg.TrustedProxies, h)
	return h
}

func (s *Server) routes(r *router) {
	r.read("/healthz", http.HandlerFunc(s.healthz))
	r.read("/readyz", http.HandlerFunc(s.readyz))
	r.read(ui.StaticPrefix, ui.StaticHandler())
	// Browsers request /favicon.ico regardless of <link rel="icon">. The
	// target is content-hashed, so the redirect must not be cached forever.
	r.read("/favicon.ico", http.RedirectHandler(ui.Asset("img/icon.svg"), http.StatusFound))

	r.read("/{$}", http.RedirectHandler("/groceries", http.StatusFound))
	r.read("/recipes", s.placeholder(ui.NavRecipes, "page.recipes.title"))
	r.read("/plan", s.placeholder(ui.NavPlan, "page.plan.title"))
	r.read("/groceries", s.placeholder(ui.NavGroceries, "page.groceries.title"))
	r.read("/settings", s.placeholder(ui.NavSettings, "page.settings.title"))

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

func (s *Server) placeholder(nav ui.NavItem, titleKey i18n.Key) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := ui.Page{Title: i18n.T(r.Context(), titleKey), Active: nav}
		s.render(w, r, http.StatusOK, ui.Placeholder(p))
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := ui.Page{Title: i18n.T(ctx, "error.notFound.title")}
	s.render(w, r, http.StatusNotFound, ui.ErrorPage(p, i18n.T(ctx, "error.notFound.body")))
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
