package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/store/storetest"
)

const testRedirect = "https://claude.example/api/mcp/auth_callback"

var appRevokeRe = regexp.MustCompile(`/settings/account/apps/[0-9a-f-]{36}/revoke`)

// oauthClient plays an MCP client: it registers, sends a member through
// the consent page and redeems codes.
type oauthClient struct {
	t        *testing.T
	base     string
	id       string
	verifier string
}

func (c *oauthClient) challenge() string {
	sum := sha256.Sum256([]byte(c.verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// postJSON posts body as JSON without cookies and decodes the answer.
func postJSON(t *testing.T, target string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	return doJSON(t, req)
}

// postToken posts a form to the token or revocation endpoint the way a
// browser-based client would: cross-site, no cookies.
func postToken(t *testing.T, target string, form url.Values) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://claude.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	return doJSON(t, req)
}

func doJSON(t *testing.T, req *http.Request) (int, map[string]any) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var v map[string]any
	_ = json.NewDecoder(res.Body).Decode(&v)
	return res.StatusCode, v
}

func getJSON(t *testing.T, target string) map[string]any {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	status, v := doJSON(t, req)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d", target, status)
	}
	return v
}

func registerClient(t *testing.T, base string) *oauthClient {
	t.Helper()
	status, v := postJSON(t, base+oauthRegisterPath, map[string]any{
		"client_name": "Claude", "redirect_uris": []string{testRedirect}, "token_endpoint_auth_method": "none",
	})
	if status != http.StatusCreated || v["token_endpoint_auth_method"] != "none" {
		t.Fatalf("register: %d %v", status, v)
	}
	return &oauthClient{t: t, base: base, id: v["client_id"].(string), verifier: strings.Repeat("v", 43)}
}

// authorizeQuery is the query of the authorization request.
func (c *oauthClient) authorizeQuery(scope string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {c.id}, "redirect_uri": {testRedirect}, "state": {"xyz"},
		"code_challenge": {c.challenge()}, "code_challenge_method": {"S256"}, "scope": {scope},
		"resource": {c.base + mcpPath},
	}
}

// approve sends member through consent with the scope they pick and
// returns the code.
func (c *oauthClient) approve(member *browser, scope string) string {
	c.t.Helper()
	q := c.authorizeQuery("read write")
	member.get(oauthAuthorizePath+"?"+q.Encode()).expect(c.t, "consent page", http.StatusOK)
	q.Set("scope", scope)
	q.Set("decision", "allow")
	p := member.post(oauthAuthorizePath, q).expect(c.t, "allow", http.StatusSeeOther)
	u, err := url.Parse(p.location)
	if err != nil || !strings.HasPrefix(p.location, testRedirect+"?") {
		c.t.Fatalf("redirect to %q", p.location)
	}
	if u.Query().Get("state") != "xyz" || u.Query().Get("iss") != c.base {
		c.t.Errorf("redirect params: %v", u.Query())
	}
	return u.Query().Get("code")
}

func (c *oauthClient) exchange(code string) (int, map[string]any) {
	c.t.Helper()
	return postToken(c.t, c.base+oauthTokenPath, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {c.id}, "code": {code},
		"redirect_uri": {testRedirect}, "code_verifier": {c.verifier}, "resource": {c.base + mcpPath},
	})
}

func (c *oauthClient) refresh(token string) (int, map[string]any) {
	c.t.Helper()
	return postToken(c.t, c.base+oauthTokenPath, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {c.id}, "refresh_token": {token},
	})
}

func meStatus(t *testing.T, base, token string) int {
	t.Helper()
	c := apiClient{t: t, base: base, token: token}
	return c.do("GET", "/api/v1/me", nil).status
}

func TestOAuthMetadata(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	household(t, srv)

	pr := getJSON(t, srv.URL+wellKnownResource+mcpPath)
	if pr["resource"] != srv.URL+mcpPath || pr["authorization_servers"].([]any)[0] != srv.URL {
		t.Errorf("protected resource metadata: %v", pr)
	}
	as := getJSON(t, srv.URL+wellKnownAuthz)
	for k, want := range map[string]string{
		"issuer": srv.URL, "authorization_endpoint": srv.URL + oauthAuthorizePath, "token_endpoint": srv.URL + oauthTokenPath,
		"registration_endpoint": srv.URL + oauthRegisterPath,
	} {
		if as[k] != want {
			t.Errorf("%s = %v, want %s", k, as[k], want)
		}
	}
	if m := as["code_challenge_methods_supported"].([]any); len(m) != 1 || m[0] != "S256" {
		t.Errorf("PKCE methods: %v", m)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodOptions, srv.URL+oauthTokenPath, nil)
	req.Header.Set("Origin", "https://claude.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight: %d %v", res.StatusCode, res.Header)
	}
}

func TestOAuthRegister(t *testing.T) {
	_, srv := startServer(t, storetest.SQLite(t))
	household(t, srv)
	for _, uris := range [][]string{
		{"http://evil.example/cb"}, {"javascript:alert(1)"}, {"https://x.example/cb#frag"}, {}, {"/relative"},
		{"https://*/cb"}, {"https://x;script-src/cb"},
	} {
		status, v := postJSON(t, srv.URL+oauthRegisterPath, map[string]any{"redirect_uris": uris})
		if status != http.StatusBadRequest || v["error"] != "invalid_redirect_uri" {
			t.Errorf("redirect_uris %v: %d %v", uris, status, v)
		}
	}
	for _, uri := range []string{"http://127.0.0.1:33418/callback", "http://localhost:6274/oauth/callback", "cursor://anysphere.cursor-mcp/oauth/callback"} {
		if status, v := postJSON(t, srv.URL+oauthRegisterPath, map[string]any{"redirect_uris": []string{uri}}); status != http.StatusCreated {
			t.Errorf("redirect_uri %s: %d %v", uri, status, v)
		}
	}
}

func TestOAuthFlow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.DB) {
		_, srv := startServer(t, db)
		anna, ben := household(t, srv)
		c := registerClient(t, srv.URL)
		q := c.authorizeQuery("read write")

		// Signed out, the consent page sends the member to sign in first.
		stranger := newBrowser(t, srv.URL)
		p := stranger.get(oauthAuthorizePath+"?"+q.Encode()).expect(t, "signed out", http.StatusFound)
		if !strings.HasPrefix(p.location, "/signin?next=") {
			t.Errorf("signed out: redirect to %q", p.location)
		}

		p = anna.get(oauthAuthorizePath+"?"+q.Encode()).expect(t, "consent", http.StatusOK)
		if !strings.Contains(p.body, "Claude") || !strings.Contains(p.body, "claude.example") {
			t.Error("consent page does not name the app and where it goes back to")
		}
		if csp := p.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://claude.example") {
			t.Errorf("consent CSP does not allow the redirect: %q", csp)
		}

		// Requests that can't go back to the client get an error page.
		bad := c.authorizeQuery("read")
		bad.Set("client_id", "0197a3c0-0000-7000-8000-000000000000")
		anna.get(oauthAuthorizePath+"?"+bad.Encode()).expect(t, "unknown client", http.StatusBadRequest)
		bad = c.authorizeQuery("read")
		bad.Set("redirect_uri", "https://evil.example/cb")
		anna.get(oauthAuthorizePath+"?"+bad.Encode()).expect(t, "unregistered redirect", http.StatusBadRequest)
		// Others go back with an error.
		bad = c.authorizeQuery("read")
		bad.Del("code_challenge")
		p = anna.get(oauthAuthorizePath+"?"+bad.Encode()).expect(t, "no PKCE", http.StatusSeeOther)
		if !strings.Contains(p.location, "error=invalid_request") || !strings.Contains(p.location, "state=xyz") {
			t.Errorf("no PKCE: redirect to %q", p.location)
		}

		deny := c.authorizeQuery("read")
		deny.Set("decision", "deny")
		p = anna.post(oauthAuthorizePath, deny).expect(t, "deny", http.StatusSeeOther)
		if !strings.Contains(p.location, "error=access_denied") {
			t.Errorf("deny: redirect to %q", p.location)
		}

		// Anna allows reading only, though the app asked to write.
		code := c.approve(anna, "read")
		wrong := *c
		wrong.verifier = strings.Repeat("w", 43)
		if status, v := wrong.exchange(code); status != http.StatusBadRequest || v["error"] != "invalid_grant" {
			t.Errorf("wrong verifier: %d %v", status, v)
		}
		status, tok := c.exchange(code)
		if status != http.StatusOK || tok["token_type"] != "Bearer" || tok["scope"] != "read" {
			t.Fatalf("exchange: %d %v", status, tok)
		}
		access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
		if !strings.HasPrefix(access, "mise_at_") || !strings.HasPrefix(refresh, "mise_rt_") {
			t.Errorf("token prefixes: %s %s", access, refresh)
		}
		if meStatus(t, srv.URL, access) != http.StatusOK {
			t.Error("access token does not open the API")
		}
		cs := mcpSession(t, srv.URL, access)
		var me whoamiJSON
		callTool(t, cs, "whoami", map[string]any{}, &me)
		if me.User.Username != "anna" || me.Scope != "read" {
			t.Errorf("whoami: %+v", me)
		}

		// The code works once; replaying it ends the grant.
		if status, _ := c.exchange(code); status != http.StatusBadRequest {
			t.Errorf("replayed code: %d", status)
		}
		if meStatus(t, srv.URL, access) != http.StatusUnauthorized {
			t.Error("grant survived a replayed code")
		}

		// Refresh rotates both tokens; replaying the old refresh token ends
		// the grant.
		_, tok = c.exchange(c.approve(anna, "write"))
		access, refresh = tok["access_token"].(string), tok["refresh_token"].(string)
		status, next := c.refresh(refresh)
		if status != http.StatusOK || next["scope"] != "read write" {
			t.Fatalf("refresh: %d %v", status, next)
		}
		if meStatus(t, srv.URL, access) != http.StatusUnauthorized || meStatus(t, srv.URL, next["access_token"].(string)) != http.StatusOK {
			t.Error("refresh did not replace the access token")
		}
		other := registerClient(t, srv.URL)
		if status, _ := other.refresh(next["refresh_token"].(string)); status != http.StatusBadRequest {
			t.Error("another client used the refresh token")
		}
		if status, _ := c.refresh(refresh); status != http.StatusBadRequest {
			t.Error("old refresh token worked again")
		}
		if meStatus(t, srv.URL, next["access_token"].(string)) != http.StatusUnauthorized {
			t.Error("grant survived a replayed refresh token")
		}

		// A client with one redirect URI may leave it out of both requests.
		q = c.authorizeQuery("read")
		q.Del("redirect_uri")
		anna.get(oauthAuthorizePath+"?"+q.Encode()).expect(t, "consent without redirect_uri", http.StatusOK)
		q.Set("decision", "allow")
		p = anna.post(oauthAuthorizePath, q).expect(t, "allow without redirect_uri", http.StatusSeeOther)
		u, err := url.Parse(p.location)
		if err != nil || !strings.HasPrefix(p.location, testRedirect+"?") {
			t.Fatalf("without redirect_uri: redirect to %q", p.location)
		}
		status, tok = postToken(t, srv.URL+oauthTokenPath, url.Values{
			"grant_type": {"authorization_code"}, "client_id": {c.id}, "code": {u.Query().Get("code")}, "code_verifier": {c.verifier},
		})
		if status != http.StatusOK {
			t.Fatalf("exchange without redirect_uri: %d %v", status, tok)
		}
		postToken(t, srv.URL+oauthRevokePath, url.Values{"client_id": {c.id}, "token": {tok["access_token"].(string)}})

		// Revocation ends the grant.
		_, tok = c.exchange(c.approve(anna, "read"))
		if status, _ := postToken(t, srv.URL+oauthRevokePath, url.Values{"client_id": {c.id}, "token": {tok["refresh_token"].(string)}}); status != http.StatusOK {
			t.Errorf("revoke: %d", status)
		}
		if meStatus(t, srv.URL, tok["access_token"].(string)) != http.StatusUnauthorized {
			t.Error("revoked grant still works")
		}

		// The account page lists approved apps and disconnects them. Ben's
		// grants are his own.
		_, tok = c.exchange(c.approve(anna, "read"))
		_, benTok := c.exchange(c.approve(ben, "read"))
		page := anna.get(tokensPath).expect(t, "tokens page", http.StatusOK)
		m := appRevokeRe.FindStringSubmatch(page.body)
		if m == nil || strings.Count(page.body, "/settings/account/apps/") != 1 {
			t.Fatalf("tokens page lists %d apps", strings.Count(page.body, "/settings/account/apps/"))
		}
		ben.post(m[0], nil).expect(t, "ben disconnects anna's app", http.StatusNotFound)
		anna.post(m[0], nil).expect(t, "disconnect", http.StatusSeeOther, tokensPath+"?done=appRevoked")
		if meStatus(t, srv.URL, tok["access_token"].(string)) != http.StatusUnauthorized {
			t.Error("disconnected app still works")
		}
		if meStatus(t, srv.URL, benTok["access_token"].(string)) != http.StatusOK {
			t.Error("disconnecting anna's app ended ben's")
		}
	})
}
