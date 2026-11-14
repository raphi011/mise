package ai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/ai"
)

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.215.14:443":           true,
		"93.184.215.14:80":            true,
		"[2606:2800:21f:cb07::1]:443": true,
		"93.184.215.14:8080":          false,
		"127.0.0.1:80":                false,
		"10.1.2.3:80":                 false,
		"172.16.0.1:80":               false,
		"192.168.1.1:443":             false,
		"169.254.169.254:80":          false,
		"100.64.0.1:80":               false,
		"0.1.2.3:80":                  false,
		"224.0.0.1:80":                false,
		"255.255.255.255:80":          false,
		"[::1]:443":                   false,
		"[fd00::1]:443":               false,
		"[fe80::1]:443":               false,
		"[::ffff:127.0.0.1]:443":      false,
		"[::ffff:93.184.215.14]:443":  false,
		"[64:ff9b::7f00:1]:443":       false,
		"[2002:7f00:1::]:443":         false,
		"[::]:443":                    false,
	} {
		if got := ai.PublicAddr(netip.MustParseAddrPort(addr)); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
}

// fetcher allows dialling only the given test servers.
func fetcher(t *testing.T, timeout time.Duration, allow ...*httptest.Server) *ai.Fetcher {
	t.Helper()
	ports := map[uint16]bool{}
	for _, s := range allow {
		u, _ := url.Parse(s.URL)
		p, _ := strconv.Atoi(u.Port())
		ports[uint16(p)] = true
	}
	return ai.NewFetcher().AllowForTest(func(ap netip.AddrPort) bool {
		return ap.Addr().IsLoopback() && ports[ap.Port()]
	}, timeout)
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestFetchPage(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/recipe", http.StatusFound)
		case "/recipe":
			if r.Header.Get("User-Agent") == "" {
				t.Error("no user agent")
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<h1>Dal</h1>"))
		}
	})
	res, err := fetcher(t, 0, s).Fetch(t.Context(), s.URL+"/start", ai.KindPage)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "<h1>Dal</h1>" || res.URL.Path != "/recipe" || !strings.HasPrefix(res.ContentType, "text/html") {
		t.Errorf("got %+v", res)
	}
}

func TestFetchBlocksPrivateAddresses(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) })
	f := ai.NewFetcher() // the real check: loopback is refused
	u, _ := url.Parse(s.URL)
	for _, target := range []string{
		s.URL,
		"http://localhost:" + u.Port() + "/", // resolved, then refused at dial
		"http://[::1]:" + u.Port() + "/",
	} {
		if _, err := f.Fetch(t.Context(), target, ai.KindPage); !errors.Is(err, ai.ErrBlockedAddress) {
			t.Errorf("%s: %v, want ErrBlockedAddress", target, err)
		}
	}
	for _, bad := range []string{"ftp://example.com/", "file:///etc/passwd", "/relative", "http://user:pw@example.com/", "javascript:alert(1)"} {
		if _, err := f.Fetch(t.Context(), bad, ai.KindPage); !errors.Is(err, ai.ErrBadURL) {
			t.Errorf("%s: %v, want ErrBadURL", bad, err)
		}
	}
}

// A redirect is checked like the first request: an allowed server can't
// bounce the fetch to an internal one.
func TestFetchRechecksRedirects(t *testing.T) {
	internal := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("metadata")) })
	public := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal":
			http.Redirect(w, r, internal.URL+"/latest", http.StatusFound)
		case "/scheme":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		default: // a loop
			n, _ := strconv.Atoi(r.URL.Query().Get("n"))
			http.Redirect(w, r, "/loop?n="+strconv.Itoa(n+1), http.StatusFound)
		}
	})
	f := fetcher(t, 0, public)
	for path, want := range map[string]error{
		"/internal": ai.ErrBlockedAddress,
		"/scheme":   ai.ErrBadURL,
		"/loop":     ai.ErrTooManyRedirects,
	} {
		if _, err := f.Fetch(t.Context(), public.URL+path, ai.KindPage); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", path, err, want)
		}
	}
}

func TestFetchLimits(t *testing.T) {
	big := strings.Repeat("x", ai.MaxPageBytes+1)
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(big))
		case "/chunked-big": // no Content-Length
			w.Header().Set("Content-Type", "text/html")
			for i := 0; i < 6; i++ {
				_, _ = w.Write([]byte(strings.Repeat("y", 1<<20)))
				w.(http.Flusher).Flush()
			}
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
		case "/missing":
			http.NotFound(w, r)
		case "/slow":
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		case "/photo":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("\xff\xd8"))
		case "/api":
			if r.Header.Get("Accept") != "application/json" {
				http.Error(w, "not acceptable", http.StatusNotAcceptable)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Total", "7")
			_, _ = w.Write([]byte(`[]`))
		}
	})
	f := fetcher(t, 300*time.Millisecond, s)
	for path, want := range map[string]error{
		"/big":         ai.ErrTooLarge,
		"/chunked-big": ai.ErrTooLarge,
		"/pdf":         ai.ErrContentType,
		"/slow":        context.DeadlineExceeded,
	} {
		if _, err := f.Fetch(t.Context(), s.URL+path, ai.KindPage); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", path, err, want)
		}
	}
	var se *ai.StatusError
	if _, err := f.Fetch(t.Context(), s.URL+"/missing", ai.KindPage); !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("/missing: %v", err)
	}
	if _, err := f.Fetch(t.Context(), s.URL+"/photo", ai.KindPage); !errors.Is(err, ai.ErrContentType) {
		t.Errorf("photo as page: %v", err)
	}
	if res, err := f.Fetch(t.Context(), s.URL+"/photo", ai.KindImage); err != nil || len(res.Body) != 2 {
		t.Errorf("photo: %v", err)
	}
	// JSON is fetched only when asked for, and comes with its header.
	if _, err := f.Fetch(t.Context(), s.URL+"/api", ai.KindPage); err == nil {
		t.Error("JSON as page")
	}
	if res, err := f.Fetch(t.Context(), s.URL+"/api", ai.KindJSON); err != nil || string(res.Body) != "[]" || res.Header.Get("X-Total") != "7" {
		t.Errorf("JSON: %+v, %v", res, err)
	}
	if _, err := f.Fetch(t.Context(), s.URL+"/pdf", ai.KindJSON); !errors.Is(err, ai.ErrContentType) {
		t.Errorf("pdf as JSON: %v", err)
	}
}

// A fetch pinned to a host refuses to start on, or be redirected to,
// another host; redirects on the host are followed.
func TestFetchOnHost(t *testing.T) {
	var s *httptest.Server
	s = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, strings.Replace(s.URL, "127.0.0.1", "localhost", 1)+"/page", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/page", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("page"))
		}
	})
	f := fetcher(t, 0, s)
	if _, err := f.FetchOnHost(t.Context(), s.URL+"/away", "127.0.0.1", ai.KindPage); !errors.Is(err, ai.ErrOtherHost) {
		t.Errorf("redirect to another host: %v", err)
	}
	if _, err := f.FetchOnHost(t.Context(), s.URL+"/page", "example.org", ai.KindPage); !errors.Is(err, ai.ErrOtherHost) {
		t.Errorf("start on another host: %v", err)
	}
	if res, err := f.FetchOnHost(t.Context(), s.URL+"/here", "127.0.0.1", ai.KindPage); err != nil || string(res.Body) != "page" {
		t.Errorf("redirect on the host: %v", err)
	}
	// Unpinned, the same redirect is followed.
	if res, err := f.Fetch(t.Context(), s.URL+"/away", ai.KindPage); err != nil || string(res.Body) != "page" {
		t.Errorf("unpinned redirect: %v", err)
	}
}
