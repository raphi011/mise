// Package ssotest is a fake OpenID Connect provider for tests: discovery,
// keys and a token endpoint that checks the client secret, redirect URI
// and PKCE verifier before it signs an ID token.
package ssotest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

// Client credentials the provider accepts.
const (
	ClientID     = "mise"
	ClientSecret = "test-secret"
	keyID        = "test-key"
)

// Claims is who signs in at the provider. Zero overrides use the values
// from the authorization request.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	// Nonce replaces the request's nonce, to test a replayed token.
	Nonce string
}

// Provider is a running fake provider.
type Provider struct {
	// URL is the issuer.
	URL string

	t   *testing.T
	key *rsa.PrivateKey
	srv *httptest.Server

	mu     sync.Mutex
	down   bool
	grants map[string]grant
	// authEndpoint replaces the advertised authorization endpoint.
	authEndpoint string
}

type grant struct {
	claims      Claims
	nonce       string
	challenge   string
	redirectURI string
}

// New starts a provider, stopped when the test ends.
func New(t *testing.T) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{t: t, key: key, grants: map[string]grant{}}
	disc := &oidctest.Server{PublicKeys: []oidctest.PublicKey{{PublicKey: key.Public(), KeyID: keyID, Algorithm: oidc.RS256}}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		p.discovery(w, r, disc)
	})
	mux.Handle("/", disc)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		down := p.down
		p.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	p.URL = p.srv.URL
	disc.SetIssuer(p.URL)
	return p
}

// SetAuthorizationEndpoint advertises endpoint as the authorization
// endpoint, as providers do whose sign-in page is on another host than
// the issuer.
func (p *Provider) SetAuthorizationEndpoint(endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authEndpoint = endpoint
}

// discovery serves disc's discovery document with the authorization
// endpoint replaced when SetAuthorizationEndpoint was called.
func (p *Provider) discovery(w http.ResponseWriter, r *http.Request, disc http.Handler) {
	p.mu.Lock()
	endpoint := p.authEndpoint
	p.mu.Unlock()
	if endpoint == "" {
		disc.ServeHTTP(w, r)
		return
	}
	rec := httptest.NewRecorder()
	disc.ServeHTTP(rec, r)
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc["authorization_endpoint"] = endpoint
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// SetDown makes every endpoint answer 503 until called with false.
func (p *Provider) SetDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

// Authorize plays the user signing in at the provider: it checks the
// authorization URL the app redirected to and returns the callback URL the
// provider sends the browser back to, with a code for c.
func (p *Provider) Authorize(authURL string, c Claims) string {
	p.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": ClientID, "scope": "openid email profile", "code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			p.t.Fatalf("authorization request %s = %q, want %q", k, got, want)
		}
	}
	for _, k := range []string{"state", "nonce", "code_challenge", "redirect_uri"} {
		if q.Get(k) == "" {
			p.t.Fatalf("authorization request without %s", k)
		}
	}
	code := rand.Text()
	p.mu.Lock()
	p.grants[code] = grant{claims: c, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
	p.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		p.t.Fatal(err)
	}
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id != ClientID || secret != ClientSecret {
		tokenError(w, "invalid_client")
		return
	}
	code := r.PostFormValue("code")
	p.mu.Lock()
	g, ok := p.grants[code]
	delete(p.grants, code) // single use
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if !ok || r.PostFormValue("grant_type") != "authorization_code" || r.PostFormValue("redirect_uri") != g.redirectURI ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		tokenError(w, "invalid_grant")
		return
	}
	nonce := g.nonce
	if g.claims.Nonce != "" {
		nonce = g.claims.Nonce
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": p.URL, "aud": ClientID, "sub": g.claims.Subject, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"email": g.claims.Email, "email_verified": g.claims.EmailVerified,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access", "token_type": "Bearer", "expires_in": 3600,
		"id_token": oidctest.SignIDToken(p.key, keyID, oidc.RS256, string(claims)),
	})
}

func tokenError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
