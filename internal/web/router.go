package web

import (
	"fmt"
	"net/http"
)

// router wraps http.ServeMux and separates read routes from write routes.
//
// http.CrossOriginProtection always lets GET, HEAD and OPTIONS through, so a
// state-changing handler on a safe method would have no CSRF protection.
// write panics at startup when given a safe method.
type router struct {
	mux *http.ServeMux
	// patterns lists the registered "METHOD /path" patterns, for tests.
	patterns []string
	// callbacks lists the GET routes registered with callback, for tests.
	callbacks []string
}

func newRouter() *router { return &router{mux: http.NewServeMux()} }

// read registers a GET handler (ServeMux also answers HEAD for it).
func (r *router) read(path string, h http.Handler) {
	r.mux.Handle("GET "+path, recordRoute(h))
	r.patterns = append(r.patterns, "GET "+path)
}

// write registers a state-changing handler. method must be POST, PUT, PATCH
// or DELETE.
func (r *router) write(method, path string, h http.Handler) {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		panic(fmt.Sprintf("web: write route %s %s must use POST, PUT, PATCH or DELETE", method, path))
	}
	r.mux.Handle(method+" "+path, recordRoute(h))
	r.patterns = append(r.patterns, method+" "+path)
}

// oidcCallbackPath is where the OpenID Connect provider sends the browser
// back after sign-in.
const oidcCallbackPath = "/auth/oidc/callback"

// callback registers the one GET route that changes state: the OIDC
// callback, which the provider reaches by redirecting the browser, so it
// cannot be a POST. CrossOriginProtection does not cover it; the handler
// checks the state, nonce and PKCE verifier bound to the browser instead.
func (r *router) callback(path string, h http.Handler) {
	if path != oidcCallbackPath {
		panic(fmt.Sprintf("web: %s is not the OIDC callback; state-changing routes must use write", path))
	}
	r.read(path, h)
	r.callbacks = append(r.callbacks, "GET "+path)
}

// fallback handles every request no other route matches, for any method.
func (r *router) fallback(h http.Handler) {
	r.mux.Handle("/", recordRoute(h))
}

// prefix handles every request under a path prefix (a pattern ending in "/")
// that no other route matches, for any method.
func (r *router) prefix(path string, h http.Handler) {
	r.mux.Handle(path, recordRoute(h))
}

// pattern returns the registered pattern that would serve req, so requests
// rejected before routing are logged by route rather than by path.
func (r *router) pattern(req *http.Request) string {
	_, p := r.mux.Handler(req)
	return p
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }
