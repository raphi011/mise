package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

// mise is its own OAuth 2.1 authorization server for the MCP endpoint
// (docs/mcp.md): clients discover it from the protected-resource metadata
// (RFC 9728), register themselves (RFC 7591), send the member to the
// consent page and redeem the code with PKCE. The machine endpoints take no
// cookies, like the API, so they skip the session and cross-origin
// protection and allow any origin.

const (
	oauthAuthorizePath = "/oauth/authorize"
	oauthTokenPath     = "/oauth/token"
	oauthRegisterPath  = "/oauth/register"
	oauthRevokePath    = "/oauth/revoke"
	wellKnownAuthz     = "/.well-known/oauth-authorization-server"
	wellKnownResource  = "/.well-known/oauth-protected-resource"
	// oauthMaxBody bounds token, revocation and registration bodies.
	oauthMaxBody = 64 << 10
)

// Client registration limit per client IP: a burst of 10, then one a
// minute. Registration needs no account, so this bounds the rows a
// stranger can add; unused clients are swept after auth.UnusedClientTTL.
const (
	registerBurst    = 10
	registerInterval = time.Minute
)

// isMachine reports whether a path is an OAuth or MCP endpoint that clients
// call without cookies.
func isMachine(path string) bool {
	switch path {
	case oauthTokenPath, oauthRegisterPath, oauthRevokePath, mcpPath:
		return true
	}
	return strings.HasPrefix(path, wellKnownAuthz) || strings.HasPrefix(path, wellKnownResource)
}

// issuer is the URL the authorization server and the MCP endpoint are
// reached at: the public base URL r came in on, or r's origin before
// setup stores one.
func (s *Server) issuer(r *http.Request) string {
	if u := s.requestBaseURL(r); u != nil {
		return strings.TrimSuffix(u.String(), "/")
	}
	return requestOrigin(r, s.cfg.TrustedProxies)
}

// mcpResource is the MCP endpoint's URL, the resource OAuth tokens are for.
func (s *Server) mcpResource(r *http.Request) string { return s.issuer(r) + mcpPath }

// allowCORS lets browser-based clients read a machine endpoint. They send
// no cookies, so any origin may.
func allowCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version, Mcp-Session-Id, Last-Event-ID")
	h.Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id, Mcp-Protocol-Version")
	h.Set("Access-Control-Max-Age", "86400")
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowCORS(w)
		h.ServeHTTP(w, r)
	})
}

func preflight(w http.ResponseWriter, _ *http.Request) {
	allowCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

// oauthScopes are the OAuth scope names: read, and write for read and
// write tokens.
var oauthScopes = []string{"read", "write"}

func scopeString(sc auth.TokenScope) string {
	if sc == auth.ScopeWrite {
		return "read write"
	}
	return "read"
}

// requestedScope picks the scope the consent page preselects: write when
// the client asks for it, read otherwise.
func requestedScope(scope string) auth.TokenScope {
	if slices.Contains(strings.Fields(scope), "write") {
		return auth.ScopeWrite
	}
	return auth.ScopeRead
}

func (s *Server) authzServerMetadata(w http.ResponseWriter, r *http.Request) {
	iss := s.issuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         iss,
		"authorization_endpoint":                         iss + oauthAuthorizePath,
		"token_endpoint":                                 iss + oauthTokenPath,
		"registration_endpoint":                          iss + oauthRegisterPath,
		"revocation_endpoint":                            iss + oauthRevokePath,
		"scopes_supported":                               oauthScopes,
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func (s *Server) protectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.mcpResource(r),
		"resource_name":            "mise",
		"authorization_servers":    []string{s.issuer(r)},
		"scopes_supported":         oauthScopes,
		"bearer_methods_supported": []string{"header"},
	})
}

// oauthFail answers a token, revocation or registration request with an
// OAuth error.
func oauthFail(w http.ResponseWriter, status int, code, description string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="mise"`)
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// registerClient implements dynamic client registration for public
// clients. Whatever authentication method the client asks for, it is
// registered as "none": it proves itself with PKCE.
func (s *Server) registerClient(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.registerLimit.Allow(clientIP(r, s.cfg.TrustedProxies).String()) {
		oauthFail(w, http.StatusTooManyRequests, "slow_down", "Too many registrations; try again in a minute.")
		return
	}
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		oauthFail(w, http.StatusBadRequest, "invalid_client_metadata", "The body must be a JSON client metadata document.")
		return
	}
	c, err := s.auth.RegisterClient(r.Context(), in.ClientName, in.RedirectURIs)
	var me *auth.ClientMetadataError
	if errors.As(err, &me) {
		oauthFail(w, http.StatusBadRequest, me.Code, me.Description)
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "oauth register", slog.Any("err", err))
		oauthFail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID.String(),
		"client_id_issued_at":        s.now().Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      strings.Join(oauthScopes, " "),
	})
}

// clientOf reads the client_id of a token or revocation request, from the
// form or from HTTP Basic credentials.
func clientOf(r *http.Request) (uuid.UUID, bool) {
	raw := r.PostFormValue("client_id")
	if user, _, ok := r.BasicAuth(); ok && raw == "" {
		raw = user
	}
	id, err := uuid.Parse(raw)
	return id, err == nil
}

// token is the token endpoint: authorization_code and refresh_token
// grants.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBody)
	if err := r.ParseForm(); err != nil {
		oauthFail(w, http.StatusBadRequest, "invalid_request", "The body must be form-encoded.")
		return
	}
	clientID, ok := clientOf(r)
	if !ok {
		oauthFail(w, http.StatusUnauthorized, "invalid_client", "Send the client_id you registered.")
		return
	}
	if _, err := s.auth.OAuthClientByID(ctx, clientID); errors.Is(err, auth.ErrOAuthClient) {
		oauthFail(w, http.StatusUnauthorized, "invalid_client", "The client_id is unknown; register again.")
		return
	} else if err != nil {
		s.log.ErrorContext(ctx, "oauth token", slog.Any("err", err))
		oauthFail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	if res := r.PostFormValue("resource"); res != "" && !s.validResource(r, res) {
		oauthFail(w, http.StatusBadRequest, "invalid_target", "Tokens are only issued for "+s.mcpResource(r)+".")
		return
	}
	var pair auth.TokenPair
	var err error
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		pair, err = s.auth.ExchangeCode(ctx, clientID, r.PostFormValue("code"), r.PostFormValue("redirect_uri"), r.PostFormValue("code_verifier"))
	case "refresh_token":
		pair, err = s.auth.RefreshOAuth(ctx, clientID, r.PostFormValue("refresh_token"))
	default:
		oauthFail(w, http.StatusBadRequest, "unsupported_grant_type", "Use authorization_code or refresh_token.")
		return
	}
	if errors.Is(err, auth.ErrOAuthGrant) {
		oauthFail(w, http.StatusBadRequest, "invalid_grant", "The code or refresh token is invalid, expired or revoked.")
		return
	}
	if err != nil {
		s.log.ErrorContext(ctx, "oauth token", slog.Any("err", err))
		oauthFail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  pair.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    int(pair.ExpiresIn.Seconds()),
		"refresh_token": pair.RefreshToken,
		"scope":         scopeString(pair.Scope),
	})
}

// revoke is the revocation endpoint (RFC 7009): it ends the grant of the
// token, and answers 200 for unknown tokens too.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBody)
	if err := r.ParseForm(); err != nil {
		oauthFail(w, http.StatusBadRequest, "invalid_request", "The body must be form-encoded.")
		return
	}
	clientID, ok := clientOf(r)
	if !ok {
		oauthFail(w, http.StatusUnauthorized, "invalid_client", "Send the client_id you registered.")
		return
	}
	if err := s.auth.RevokeOAuthToken(r.Context(), clientID, r.PostFormValue("token")); err != nil {
		s.log.ErrorContext(r.Context(), "oauth revoke", slog.Any("err", err))
		oauthFail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// validResource reports whether an RFC 8707 resource indicator names the
// MCP endpoint (or the server as a whole).
func (s *Server) validResource(r *http.Request, res string) bool {
	res = strings.TrimSuffix(res, "/")
	return res == s.mcpResource(r) || res == s.issuer(r)
}

// authzRequest is a validated authorization request.
type authzRequest struct {
	client auth.OAuthClient
	// redirectURI is where the browser goes back to; requestedRedirect
	// is the request's redirect_uri, which may be left out.
	redirectURI       string
	requestedRedirect string
	state             string
	challenge         string
	scope             auth.TokenScope
	params            []ui.Hidden
}

// parseAuthz validates an authorization request from the query (GET) or
// form (POST). Without a known client and redirect URI it renders an
// error page itself and returns false; other errors go back to the
// client's redirect URI.
func (s *Server) parseAuthz(w http.ResponseWriter, r *http.Request, v url.Values) (authzRequest, bool) {
	ctx := r.Context()
	var req authzRequest
	clientID, err := uuid.Parse(v.Get("client_id"))
	if err == nil {
		req.client, err = s.auth.OAuthClientByID(ctx, clientID)
	}
	if errors.Is(err, auth.ErrOAuthClient) || clientID == uuid.Nil {
		s.oauthErrorPage(w, r, i18n.T(ctx, "oauth.error.client"))
		return req, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return req, false
	}
	req.requestedRedirect = v.Get("redirect_uri")
	req.redirectURI = req.client.Redirect(req.requestedRedirect)
	if !req.client.RedirectAllowed(req.redirectURI) {
		s.oauthErrorPage(w, r, i18n.T(ctx, "oauth.error.redirect"))
		return req, false
	}
	req.state = v.Get("state")
	fail := func(code, description string) (authzRequest, bool) {
		s.authzRedirect(w, r, req, url.Values{"error": {code}, "error_description": {description}})
		return req, false
	}
	if v.Get("response_type") != "code" {
		return fail("unsupported_response_type", "Only response_type=code is supported.")
	}
	req.challenge = v.Get("code_challenge")
	if v.Get("code_challenge_method") != "S256" || !auth.ValidCodeChallenge(req.challenge) {
		return fail("invalid_request", "PKCE with code_challenge_method=S256 is required.")
	}
	if res := v.Get("resource"); res != "" && !s.validResource(r, res) {
		return fail("invalid_target", "Tokens are only issued for "+s.mcpResource(r)+".")
	}
	req.scope = requestedScope(v.Get("scope"))
	for _, name := range []string{"client_id", "redirect_uri", "state", "response_type", "code_challenge", "code_challenge_method", "resource"} {
		if val := v.Get(name); val != "" {
			req.params = append(req.params, ui.Hidden{Name: name, Value: val})
		}
	}
	return req, true
}

// authzRedirect sends the browser back to the client with params, the
// state and the issuer (RFC 9207).
func (s *Server) authzRedirect(w http.ResponseWriter, r *http.Request, req authzRequest, params url.Values) {
	u, err := url.Parse(req.redirectURI)
	if err != nil {
		s.oauthErrorPage(w, r, i18n.T(r.Context(), "oauth.error.redirect"))
		return
	}
	q := u.Query()
	for k, vs := range params {
		q[k] = vs
	}
	if req.state != "" {
		q.Set("state", req.state)
	}
	q.Set("iss", s.issuer(r))
	u.RawQuery = q.Encode()
	seeOther(w, r, u.String())
}

func (s *Server) oauthErrorPage(w http.ResponseWriter, r *http.Request, message string) {
	p := s.page(r, i18n.T(r.Context(), "oauth.error.title"), ui.NavNone)
	s.render(w, r, http.StatusBadRequest, ui.OAuthError(p, message))
}

// redirectFormSource is the CSP source that lets the consent form's
// response redirect to uri: browsers check form-action against the
// redirects a submission leads to as well.
func redirectFormSource(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + ":"
}

func (s *Server) consentPage(w http.ResponseWriter, r *http.Request) {
	req, ok := s.parseAuthz(w, r, r.URL.Query())
	if !ok {
		return
	}
	s.renderConsent(w, r, http.StatusOK, req, "")
}

func (s *Server) renderConsent(w http.ResponseWriter, r *http.Request, status int, req authzRequest, alert string) {
	ctx := r.Context()
	id := identity(r)
	host := req.redirectURI
	if u, err := url.Parse(req.redirectURI); err == nil && u.Host != "" {
		host = u.Host
	}
	v := ui.ConsentView{
		Client: req.client.Name, Host: host, Household: id.HouseholdName, UserName: id.Name,
		Scope: string(req.scope), Params: req.params, Alert: alert,
		Scopes: []ui.Option{
			{Value: string(auth.ScopeRead), Label: i18n.T(ctx, "tokens.scope.read"), Hint: i18n.T(ctx, "tokens.scope.readHint")},
			{Value: string(auth.ScopeWrite), Label: i18n.T(ctx, "tokens.scope.write"), Hint: i18n.T(ctx, "tokens.scope.writeHint")},
		},
	}
	if src := redirectFormSource(req.redirectURI); src != "" {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy(src))
	}
	w.Header().Set("Cache-Control", "no-store")
	p := s.page(r, i18n.T(ctx, "oauth.consent.pageTitle"), ui.NavNone)
	s.render(w, r, status, ui.Consent(p, v))
}

func (s *Server) consentSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.oauthErrorPage(w, r, i18n.T(ctx, "oauth.error.client"))
		return
	}
	req, ok := s.parseAuthz(w, r, r.PostForm)
	if !ok {
		return
	}
	if r.PostFormValue("decision") != "allow" {
		s.authzRedirect(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"The member declined."}})
		return
	}
	req.scope = auth.TokenScope(r.PostFormValue("scope"))
	if !req.scope.Valid() {
		req.scope = auth.ScopeRead
		s.renderConsent(w, r, http.StatusUnprocessableEntity, req, i18n.T(ctx, "oauth.consent.pickScope"))
		return
	}
	code, err := s.auth.Authorize(ctx, identity(r), auth.AuthorizeInput{
		ClientID: req.client.ID, RedirectURI: req.requestedRedirect, CodeChallenge: req.challenge, Scope: req.scope,
	})
	if errors.Is(err, auth.ErrTooManyGrants) {
		s.renderConsent(w, r, http.StatusUnprocessableEntity, req, i18n.T(ctx, "oauth.consent.tooMany"))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.authzRedirect(w, r, req, url.Values{"code": {code}})
}

// oauthRoutes registers the authorization server.
func (s *Server) oauthRoutes(r *router) {
	r.read(wellKnownAuthz, cors(http.HandlerFunc(s.authzServerMetadata)))
	r.read(wellKnownAuthz+"/", cors(http.HandlerFunc(s.authzServerMetadata)))
	r.read(wellKnownResource, cors(http.HandlerFunc(s.protectedResourceMetadata)))
	r.read(wellKnownResource+"/", cors(http.HandlerFunc(s.protectedResourceMetadata)))
	r.read(oauthAuthorizePath, s.user(s.consentPage))
	r.write(http.MethodPost, oauthAuthorizePath, s.user(s.consentSubmit))
	r.write(http.MethodPost, oauthRegisterPath, cors(http.HandlerFunc(s.registerClient)))
	r.write(http.MethodPost, oauthTokenPath, cors(http.HandlerFunc(s.token)))
	r.write(http.MethodPost, oauthRevokePath, cors(http.HandlerFunc(s.revoke)))
	for _, p := range []string{wellKnownAuthz, wellKnownAuthz + "/", wellKnownResource, wellKnownResource + "/",
		oauthRegisterPath, oauthTokenPath, oauthRevokePath, mcpPath} {
		r.preflight(p, http.HandlerFunc(preflight))
	}
}
