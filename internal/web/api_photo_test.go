package web

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/raphi011/mise/internal/ai"
	"github.com/raphi011/mise/internal/images"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

// raw sends body as is, with contentType.
func (c apiClient) raw(method, path, contentType string, body []byte) apiResult {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", contentType)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return apiResult{status: res.StatusCode, header: res.Header, body: b}
}

// fieldError returns the message of a field in an API error.
func fieldError(t *testing.T, r apiResult, field string) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	fields, _ := e["fields"].(map[string]any)
	msg, _ := fields[field].(string)
	return msg
}

var heicBytes = append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 64)...)

func TestAPIRecipePhoto(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, _ := household(t, srv)
		api := apiClient{t: t, base: srv.URL, token: makeToken(t, anna, "agent", "write")}
		read := apiClient{t: t, base: srv.URL, token: makeToken(t, anna, "reader", "read")}

		rec := api.want(api.do("POST", "/api/v1/recipes", map[string]any{"name": "Dal", "servings": 2}), http.StatusCreated).json(t)
		id := rec["id"].(string)
		path := "/api/v1/recipes/" + id + "/photo"
		if rec["photo_url"] != nil {
			t.Errorf("new recipe has a photo: %v", rec["photo_url"])
		}
		read.want(read.do("GET", path, nil), http.StatusNotFound)

		// A read token can't set or remove it.
		if errorCode(t, read.want(read.raw("PUT", path, "image/png", pngBytes(t)), http.StatusForbidden)) != "insufficient_scope" {
			t.Error("read token set a photo")
		}

		// Rejected images get the editor's messages.
		r := api.want(api.raw("PUT", path, "image/png", []byte("not an image")), http.StatusUnprocessableEntity)
		if !strings.Contains(fieldError(t, r, "photo"), "JPEG, PNG or WebP") {
			t.Errorf("invalid image: %s", r.body)
		}
		r = api.want(api.raw("PUT", path, "image/heic", heicBytes), http.StatusUnprocessableEntity)
		if !strings.Contains(fieldError(t, r, "photo"), "HEIC") {
			t.Errorf("heic: %s", r.body)
		}
		r = api.want(api.raw("PUT", path, "image/jpeg", make([]byte, images.MaxUploadBytes+1)), http.StatusRequestEntityTooLarge)
		if errorCode(t, r) != "too_large" || !strings.Contains(fieldError(t, r, "photo"), "15 MB") {
			t.Errorf("oversize: %s", r.body)
		}
		api.want(api.raw("PUT", "/api/v1/recipes/00000000-0000-0000-0000-000000000001/photo", "image/png", pngBytes(t)), http.StatusNotFound)
		if got := api.want(api.do("GET", "/api/v1/recipes/"+id, nil), http.StatusOK).json(t); got["photo_url"] != nil {
			t.Errorf("rejected photo set: %v", got["photo_url"])
		}

		set := api.want(api.raw("PUT", path, "image/png", pngBytes(t)), http.StatusOK).json(t)
		if set["photo_url"] != path {
			t.Fatalf("photo_url %v, want %s", set["photo_url"], path)
		}
		list := api.want(api.do("GET", "/api/v1/recipes", nil), http.StatusOK).json(t)
		if card := list["recipes"].([]any)[0].(map[string]any); card["photo_url"] != path {
			t.Errorf("card photo_url %v", card["photo_url"])
		}
		img := read.want(read.do("GET", path, nil), http.StatusOK)
		if img.header.Get("Content-Type") != "image/jpeg" || img.header.Get("ETag") == "" || !bytes.HasPrefix(img.body, []byte{0xff, 0xd8}) {
			t.Errorf("photo: %v %.20q", img.header, img.body)
		}
		thumb := read.want(read.do("GET", path+"?size=thumb", nil), http.StatusOK)
		if thumb.header.Get("ETag") == img.header.Get("ETag") {
			t.Error("thumb has the full size's ETag")
		}
		read.want(read.do("GET", path+"?size=huge", nil), http.StatusNotFound)
		// The web UI shows it.
		if !photoRe.MatchString(anna.get("/recipes").body) {
			t.Error("photo missing from the library")
		}

		// Replacing the recipe keeps the photo; it has its own endpoints.
		upd := api.want(api.do("PUT", "/api/v1/recipes/"+id, map[string]any{"name": "Dal tadka", "servings": 2}), http.StatusOK).json(t)
		if upd["photo_url"] != path {
			t.Errorf("update dropped the photo: %v", upd["photo_url"])
		}

		read.want(read.do("DELETE", path, nil), http.StatusForbidden)
		api.want(api.do("DELETE", path, nil), http.StatusNoContent)
		if got := api.want(api.do("GET", "/api/v1/recipes/"+id, nil), http.StatusOK).json(t); got["photo_url"] != nil {
			t.Errorf("photo not removed: %v", got["photo_url"])
		}
		read.want(read.do("GET", path, nil), http.StatusNotFound)
		api.want(api.do("DELETE", path, nil), http.StatusNoContent)
		api.want(api.do("DELETE", "/api/v1/recipes/00000000-0000-0000-0000-000000000001/photo", nil), http.StatusNotFound)
	})
}

// photoServer serves https from loopback: a PNG at /photo.png and the
// other paths' test cases.
func photoServer(t *testing.T, png []byte, other *httptest.Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/photo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<h1>Dal</h1>"))
		case "/huge":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Length", strconv.Itoa(images.MaxUploadBytes+1))
			_, _ = w.Write(make([]byte, images.MaxUploadBytes+1))
		case "/to-internal":
			http.Redirect(w, r, other.URL+"/secret", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, strings.Replace(other.URL, "https://", "http://", 1)+"/secret", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMCPRecipePhoto(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		s, srv := startServer(t, db)
		anna, _ := household(t, srv)
		png := pngBytes(t)

		reader := mcpSession(t, srv.URL, makeToken(t, anna, "reader", "read"))
		if names := toolNames(t, reader); slices.Contains(names, "set_recipe_photo") || slices.Contains(names, "remove_recipe_photo") {
			t.Errorf("read token tools: %v", names)
		}

		cs := mcpSession(t, srv.URL, makeToken(t, anna, "writer", "write"))
		var rec recipeJSON
		callTool(t, cs, "create_recipe", map[string]any{"name": "Dal", "servings": 2}, &rec)
		b64 := base64.StdEncoding.EncodeToString(png)

		for _, tc := range []struct {
			name string
			args map[string]any
			want string
		}{
			{"neither", map[string]any{}, "- image_base64: Send either"},
			{"both", map[string]any{"image_base64": b64, "image_url": "https://example.com/a.png"}, "- image_url: Send either"},
			{"bad base64", map[string]any{"image_base64": "!!!"}, "- image_base64: The image data isn't valid base64"},
			{"not an image", map[string]any{"image_base64": base64.StdEncoding.EncodeToString([]byte("hello"))}, "- image_base64: That file isn't"},
			{"heic", map[string]any{"image_base64": base64.StdEncoding.EncodeToString(heicBytes)}, "- image_base64: HEIC"},
			{"http", map[string]any{"image_url": "http://example.com/a.png"}, "- image_url: Use an https:// link"},
			{"not a url", map[string]any{"image_url": "file:///etc/passwd"}, "- image_url: Use an https:// link"},
		} {
			tc.args["id"] = rec.ID
			if msg := callTool(t, cs, "set_recipe_photo", tc.args, nil); !strings.Contains(msg, tc.want) {
				t.Errorf("%s: %q, want %q", tc.name, msg, tc.want)
			}
		}
		if msg := callTool(t, cs, "set_recipe_photo", map[string]any{"id": "0197a3c0-0000-7000-8000-000000000000", "image_base64": b64}, nil); !strings.Contains(msg, "Not found") {
			t.Errorf("unknown recipe: %q", msg)
		}

		// The real fetcher refuses non-public addresses before connecting.
		for _, target := range []string{
			"https://127.0.0.1/a.png", "https://localhost/a.png", "https://[::1]/a.png", "https://10.0.0.1/a.png",
			"https://192.168.30.2/a.png", "https://169.254.169.254/latest", "https://100.100.100.100/a.png", "https://[fd00::1]/a.png",
			"https://example.com:8443/a.png",
		} {
			if msg := callTool(t, cs, "set_recipe_photo", map[string]any{"id": rec.ID, "image_url": target}, nil); !strings.Contains(msg, "- image_url: That address can't be used") {
				t.Errorf("%s: %q", target, msg)
			}
		}

		var got recipeJSON
		callTool(t, cs, "set_recipe_photo", map[string]any{"id": rec.ID, "image_base64": b64}, &got)
		if got.PhotoURL == nil || *got.PhotoURL != "/api/v1/recipes/"+rec.ID+"/photo" {
			t.Fatalf("base64 photo: %v", got.PhotoURL)
		}
		callTool(t, cs, "remove_recipe_photo", map[string]any{"id": rec.ID}, &mcpDone{})
		callTool(t, cs, "get_recipe", map[string]any{"id": rec.ID}, &got)
		if got.PhotoURL != nil {
			t.Errorf("photo not removed: %v", *got.PhotoURL)
		}
		// A data: URL works too.
		callTool(t, cs, "set_recipe_photo", map[string]any{"id": rec.ID, "image_base64": "data:image/png;base64," + b64}, &got)
		if got.PhotoURL == nil {
			t.Error("data URL photo not set")
		}

		// A request over the MCP body limit is refused outright, and the
		// client gives up its session.
		big := base64.StdEncoding.EncodeToString(make([]byte, apiMaxBody))
		if _, err := mcpSession(t, srv.URL, makeToken(t, anna, "big", "write")).CallTool(t.Context(), &mcp.CallToolParams{Name: "set_recipe_photo", Arguments: map[string]any{"id": rec.ID, "image_base64": big}}); err == nil {
			t.Error("request over the body limit accepted")
		}

		// Downloads: the test fetcher may dial only the two test servers on
		// loopback, and trusts their certificates.
		internal := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		}))
		t.Cleanup(internal.Close)
		photos := photoServer(t, png, internal)
		u, _ := url.Parse(photos.URL)
		port, _ := strconv.Atoi(u.Port())
		pool := x509.NewCertPool()
		pool.AddCert(photos.Certificate())
		pool.AddCert(internal.Certificate())
		s.fetcher = ai.NewFetcher().AllowForTest(func(ap netip.AddrPort) bool {
			return ap.Addr().IsLoopback() && ap.Port() == uint16(port)
		}, 5*time.Second).TrustForTest(pool)

		for path, want := range map[string]string{
			"/page":        "- image_url: That file isn't",
			"/huge":        "- image_url: That photo is larger than 15 MB",
			"/missing":     "- image_url: The image couldn't be downloaded",
			"/to-internal": "- image_url: That address can't be used",
			"/to-http":     "- image_url: Use an https:// link",
		} {
			if msg := callTool(t, cs, "set_recipe_photo", map[string]any{"id": rec.ID, "image_url": photos.URL + path}, nil); !strings.Contains(msg, want) {
				t.Errorf("%s: %q, want %q", path, msg, want)
			}
		}
		callTool(t, cs, "remove_recipe_photo", map[string]any{"id": rec.ID}, &mcpDone{})
		callTool(t, cs, "set_recipe_photo", map[string]any{"id": rec.ID, "image_url": photos.URL + "/photo.png"}, &got)
		if got.PhotoURL == nil {
			t.Error("downloaded photo not set")
		}
	})
}
