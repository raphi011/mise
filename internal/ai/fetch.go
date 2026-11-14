package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Fetcher limits.
const (
	// MaxPageBytes caps a fetched page; MaxImageBytes a recipe photo, like
	// an upload.
	MaxPageBytes  = 5 << 20
	MaxImageBytes = 15 << 20
	fetchTimeout  = 15 * time.Second
	maxRedirects  = 5
	userAgent     = "mise-recipe-import/1"
)

// Fetch errors. Callers turn them into import error codes.
var (
	// ErrBadURL: not an absolute http or https URL.
	ErrBadURL = errors.New("ai: not an http or https URL")
	// ErrBlockedAddress: the host resolves to a loopback, private,
	// link-local or otherwise non-public address, or the port is not 80
	// or 443.
	ErrBlockedAddress = errors.New("ai: address not allowed")
	// ErrTooLarge: the body exceeds the limit.
	ErrTooLarge = errors.New("ai: response too large")
	// ErrContentType: not a page (or not an image, for photos).
	ErrContentType = errors.New("ai: unsupported content type")
	// ErrTooManyRedirects: more than five redirects.
	ErrTooManyRedirects = errors.New("ai: too many redirects")
	// ErrOtherHost: a fetch pinned to one host was redirected to another.
	ErrOtherHost = errors.New("ai: redirected to another host")
)

// pinnedHost is the context key of the host a fetch must stay on.
type pinnedHost struct{}

// StatusError is a response with a status other than 200.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return "ai: HTTP status " + strconv.Itoa(e.Code) }

// Kind is what a fetch expects.
type Kind int

// Kinds.
const (
	KindPage Kind = iota
	KindImage
)

// Fetched is a fetched body.
type Fetched struct {
	// URL is the final URL after redirects.
	URL         *url.URL
	ContentType string
	Body        []byte
}

// Fetcher fetches recipe pages and photos from the public internet only:
// the address actually dialled is checked (so DNS rebinding can't slip
// past), redirects are re-checked, and bodies and time are capped. It must not be used for the admin-configured
// provider endpoint, which is often on the local network.
type Fetcher struct {
	client *http.Client
	// allowed decides whether an address may be dialled.
	allowed func(netip.AddrPort) bool
	// timeout bounds a whole fetch, redirects and body included.
	timeout time.Duration
}

// NewFetcher returns a Fetcher.
func NewFetcher() *Fetcher {
	f := &Fetcher{allowed: publicAddr, timeout: fetchTimeout}
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !f.allowed(ap) {
				return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
			}
			return nil
		},
	}
	f.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil, // a proxy would dial for us, unchecked
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			MaxIdleConns:           10,
			IdleConnTimeout:        30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return ErrTooManyRedirects
			}
			if host, ok := req.Context().Value(pinnedHost{}).(string); ok && !strings.EqualFold(req.URL.Hostname(), host) {
				return ErrOtherHost
			}
			return checkURL(req.URL)
		},
	}
	return f
}

// AllowForTest lets f dial addresses allowed by fn and sets a shorter
// timeout when non-zero. Tests serve pages from loopback on random ports,
// which the fetcher refuses; never call it outside tests.
func (f *Fetcher) AllowForTest(fn func(netip.AddrPort) bool, timeout time.Duration) *Fetcher {
	f.allowed = fn
	if timeout > 0 {
		f.timeout = timeout
	}
	return f
}

// Fetch gets rawURL, expecting a page (HTML, XHTML or plain text) or an
// image.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, kind Kind) (Fetched, error) {
	return f.fetch(ctx, rawURL, kind, "")
}

// FetchOnHost is Fetch for a page or image that must be on host,
// redirects included (ErrOtherHost), as fetch_page's pages must be on the
// source page's host.
func (f *Fetcher) FetchOnHost(ctx context.Context, rawURL, host string, kind Kind) (Fetched, error) {
	return f.fetch(ctx, rawURL, kind, host)
}

func (f *Fetcher) fetch(ctx context.Context, rawURL string, kind Kind, host string) (Fetched, error) {
	u, err := ParseURL(rawURL)
	if err != nil {
		return Fetched{}, err
	}
	if host != "" {
		if !strings.EqualFold(u.Hostname(), host) {
			return Fetched{}, ErrOtherHost
		}
		ctx = context.WithValue(ctx, pinnedHost{}, host)
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Fetched{}, ErrBadURL
	}
	req.Header.Set("User-Agent", userAgent)
	limit := int64(MaxPageBytes)
	if kind == KindImage {
		limit = MaxImageBytes
		req.Header.Set("Accept", "image/jpeg,image/png,image/webp;q=0.9,*/*;q=0.1")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")
	}
	res, err := f.client.Do(req)
	if err != nil {
		return Fetched{}, unwrapFetchErr(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Fetched{}, &StatusError{Code: res.StatusCode}
	}
	ct := res.Header.Get("Content-Type")
	if !acceptable(ct, kind) {
		return Fetched{}, fmt.Errorf("%w: %q", ErrContentType, ct)
	}
	if res.ContentLength > limit {
		return Fetched{}, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return Fetched{}, unwrapFetchErr(err)
	}
	if int64(len(body)) > limit {
		return Fetched{}, ErrTooLarge
	}
	return Fetched{URL: res.Request.URL, ContentType: ct, Body: body}, nil
}

// unwrapFetchErr surfaces our sentinel errors from inside url.Error and
// net.OpError.
func unwrapFetchErr(err error) error {
	for _, target := range []error{ErrBlockedAddress, ErrBadURL, ErrTooManyRedirects, ErrOtherHost} {
		if errors.Is(err, target) {
			return fmt.Errorf("%w: %w", target, err)
		}
	}
	return fmt.Errorf("%w: %w", errFetch, err)
}

// errFetch wraps network failures of a fetch.
var errFetch = errors.New("ai: fetch")

// ParseURL accepts an absolute http or https URL without credentials. The
// port is checked when dialling, with the address.
func ParseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, ErrBadURL
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

func checkURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ErrBadURL
	}
	return nil
}

// acceptable reports whether a content type fits the kind of fetch.
func acceptable(contentType string, kind Kind) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	if kind == KindImage {
		return mt == "image/jpeg" || mt == "image/png" || mt == "image/webp"
	}
	return mt == "text/html" || mt == "application/xhtml+xml" || mt == "text/plain"
}

// Special-purpose ranges that IsGlobalUnicast and IsPrivate let through.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, broadcast
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64: embeds an IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"), // local NAT64
	netip.MustParsePrefix("2001::/32"),      // Teredo: embeds an IPv4 address
	netip.MustParsePrefix("2002::/16"),      // 6to4: embeds an IPv4 address
	netip.MustParsePrefix("100::/64"),       // discard
}

// publicAddr allows public unicast addresses on ports 80 and 443.
func publicAddr(ap netip.AddrPort) bool {
	if p := ap.Port(); p != 80 && p != 443 {
		return false
	}
	a := ap.Addr()
	if a.Is4In6() || a.Zone() != "" {
		return false
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false // loopback, link-local, multicast, unspecified, RFC 1918, ULA
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
