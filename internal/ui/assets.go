// Package ui holds templ components and embedded static assets.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

// StaticPrefix is the URL prefix assets are served under.
const StaticPrefix = "/static/"

type asset struct {
	data        []byte
	contentType string
}

var (
	// hashed maps logical names ("css/app.css") to served names
	// ("css/app.1a2b3c4d.css").
	hashed = map[string]string{}
	// served maps served names to content.
	served = map[string]asset{}
)

func init() {
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(p, "static/")
		sum := sha256.Sum256(data)
		ext := path.Ext(name)
		hashedName := strings.TrimSuffix(name, ext) + "." + hex.EncodeToString(sum[:4]) + ext
		ct := mime.TypeByExtension(ext)
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		hashed[name] = hashedName
		served[hashedName] = asset{data: data, contentType: ct}
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("ui: load static assets: %v", err))
	}
}

// Asset returns the content-hashed URL of a static file, e.g.
// Asset("css/app.css") → "/static/css/app.1a2b3c4d.css". It panics on an
// unknown name so a typo fails the first render in tests.
func Asset(name string) string {
	h, ok := hashed[name]
	if !ok {
		panic("ui: unknown asset " + name)
	}
	return StaticPrefix + h
}

// StaticHandler serves hashed assets with immutable caching. Unhashed names
// are not served.
func StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := served[strings.TrimPrefix(r.URL.Path, StaticPrefix)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", a.contentType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.data))
	})
}
