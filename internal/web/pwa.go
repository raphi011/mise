package web

import (
	"bytes"
	"net/http"
	"time"

	"github.com/raphi011/mise/internal/ui"
)

// manifestJSON is built once: it only names content-hashed assets.
var manifestJSON = ui.Manifest()

// manifest serves the web app manifest that makes mise installable. It
// names hashed icon URLs, so browsers must revalidate it after an update.
func manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(manifestJSON))
}

// serviceWorker serves the install-only service worker at the root, so its
// scope covers every page.
func serviceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(ui.ServiceWorker))
}
