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
}

func newRouter() *router { return &router{mux: http.NewServeMux()} }

// read registers a GET handler (ServeMux also answers HEAD for it).
func (r *router) read(path string, h http.Handler) {
	r.mux.Handle("GET "+path, recordRoute(h))
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
}

// fallback handles every request no other route matches, for any method.
func (r *router) fallback(h http.Handler) {
	r.mux.Handle("/", recordRoute(h))
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }
