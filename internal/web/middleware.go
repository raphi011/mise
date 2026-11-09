package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/tenant"
)

// statusRecorder captures the response status for logging. It implements
// Unwrap so http.ResponseController reaches the underlying writer (needed
// for SSE flushing and per-stream deadlines).
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// reqInfo collects facts known only deep in the handler chain (matched
// route, household) for the outer logging and recovery middleware. Inner
// middleware derive new requests with WithContext, so r.Pattern and context
// values set there are invisible to the outer request.
type reqInfo struct {
	route     string
	household uuid.UUID
}

type reqInfoKey struct{}

func requestInfo(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(reqInfoKey{}).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{}
}

// recordRoute wraps a routed handler to note its pattern and household.
func recordRoute(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := requestInfo(r)
		ri.route = r.Pattern
		if id, ok := tenant.HouseholdID(r.Context()); ok {
			ri.household = id
		}
		h.ServeHTTP(w, r)
	})
}

// logRequests writes one structured log line per request. routeOf, if not
// nil, names the route of requests that middleware answered before routing.
func logRequests(log *slog.Logger, trusted []netip.Prefix, routeOf func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		ri := &reqInfo{}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, ri))
		next.ServeHTTP(rec, r)

		if ri.route == "" && routeOf != nil {
			// Answered before routing (e.g. a rejected cross-origin write).
			// Paths can carry secrets such as invite tokens, so log the route
			// they would have matched instead.
			ri.route = routeOf(r)
		}
		status := rec.status
		if status == 0 {
			// Nothing written: net/http sends an implicit 200.
			status = http.StatusOK
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("route", ri.route),
			slog.Int("status", status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", time.Since(start)),
			slog.String("client", clientIP(r, trusted).String()),
		}
		if ri.route == "" || ri.route == "/" {
			// Unmatched: the path is the only clue.
			attrs = append(attrs, slog.String("path", r.URL.Path))
		}
		if ri.household != uuid.Nil {
			attrs = append(attrs, slog.String("household", ri.household.String()))
		}
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		log.LogAttrs(r.Context(), level, "request", attrs...)
	})
}

// recoverPanics turns a handler panic into a 500 and logs it with the
// goroutine's stack.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(p)
				}
				log.ErrorContext(r.Context(), "panic",
					slog.String("route", requestInfo(r).route),
					slog.String("panic", fmt.Sprint(p)),
					slog.String("stack", string(debug.Stack())))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets headers every response should carry.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// withLocale picks the UI language: the signed-in user's choice, then the
// household default, then Accept-Language.
func withLocale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var prefs []string
		if id, ok := auth.FromContext(r.Context()); ok {
			prefs = append(prefs, id.Language, id.HouseholdLanguage)
		}
		tag := i18n.Match(append(prefs, r.Header.Get("Accept-Language"))...)
		next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), tag)))
	})
}

// clientIP returns the client address. X-Forwarded-For is only honoured when
// the direct peer is a trusted proxy; then the rightmost address that is not
// itself a trusted proxy is the client.
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := peer.Addr().Unmap()
	if !isTrusted(addr, trusted) {
		return addr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			// A malformed hop means we cannot trust anything left of it.
			return addr
		}
		a = a.Unmap()
		if !isTrusted(a, trusted) {
			return a
		}
		addr = a
	}
	return addr
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
