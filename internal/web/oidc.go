package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/sso"
	"github.com/raphi011/mise/internal/ui"
)

// Single sign-on: POST /auth/oidc/start sends the browser to the provider
// with a flow cookie holding the state, nonce and PKCE verifier; the
// provider sends it back to GET /auth/oidc/callback (see router.callback).
// POST /settings/account/sso/link runs the same flow from a signed-in
// session to link the identity to that account instead of signing in.
const (
	oidcCookie    = "mise_oidc"
	oidcCookieAge = 10 * time.Minute
)

// oidcFlow is the flow cookie's content.
type oidcFlow struct {
	sso.Flow
	Next string `json:"next,omitempty"`
	// Link marks a flow that links the identity to the signed-in user: it
	// is linkBinding of the session that started it, so the callback links
	// only to that session's account.
	Link    string `json:"l,omitempty"`
	Started int64  `json:"t"`
}

// linkBinding ties a link flow to the session that started it without
// putting the session's ID in the flow cookie.
func linkBinding(id *auth.Identity) string {
	sum := sha256.Sum256([]byte("mise oidc link\x00" + id.SessionID))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ssoAvailable reports whether the sign-in page offers single sign-on:
// it is configured and the base URL for the callback is known.
func (s *Server) ssoAvailable() bool { return s.sso != nil && s.BaseURL() != nil }

// oidcRedirectURL is where the provider sends the browser back to: the
// callback on the host the flow started on, since the flow cookie is
// host-only. Each such URL must be registered with the provider.
func (s *Server) oidcRedirectURL(r *http.Request) string {
	return s.requestBaseURL(r).JoinPath(oidcCallbackPath).String()
}

func (s *Server) setFlowCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcCookie,
		Value:    value,
		Path:     "/auth/oidc/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if !s.ssoAvailable() {
		s.notFound(w, r)
		return
	}
	flow := oidcFlow{Flow: sso.NewFlow(), Next: auth.SafeNext(r.PostFormValue("next")), Started: s.now().Unix()}
	s.sendToProvider(w, r, flow)
}

// linkSSO starts linking the signed-in user's account to the provider.
func (s *Server) linkSSO(w http.ResponseWriter, r *http.Request) {
	if !s.ssoAvailable() {
		s.notFound(w, r)
		return
	}
	flow := oidcFlow{Flow: sso.NewFlow(), Next: "/settings/account", Link: linkBinding(identity(r)), Started: s.now().Unix()}
	s.sendToProvider(w, r, flow)
}

// sendToProvider stores flow in the flow cookie and redirects to the
// provider's authorization endpoint.
func (s *Server) sendToProvider(w http.ResponseWriter, r *http.Request, flow oidcFlow) {
	ctx := r.Context()
	target, err := s.sso.AuthURL(ctx, s.oidcRedirectURL(r), flow.Flow)
	if err != nil {
		s.log.WarnContext(ctx, "oidc: start", slog.Any("err", err))
		s.oidcFailed(w, r, http.StatusServiceUnavailable, flow.Next, "oidc.error.unavailable", s.cfg.OIDC.Name)
		return
	}
	b, err := json.Marshal(flow)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setFlowCookie(w, r, base64.RawURLEncoding.EncodeToString(b), int(oidcCookieAge.Seconds()))
	seeOther(w, r, target)
}

// readFlow returns the browser's flow cookie if it is intact and recent.
func (s *Server) readFlow(r *http.Request) (oidcFlow, bool) {
	var f oidcFlow
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		return f, false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || json.Unmarshal(b, &f) != nil || f.State == "" {
		return f, false
	}
	age := s.now().Sub(time.Unix(f.Started, 0))
	return f, age >= 0 && age <= oidcCookieAge
}

// oidcCallback finishes the sign-in. It is a GET that changes state, so
// it trusts nothing but what the flow cookie binds to this browser.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.ssoAvailable() {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	// One attempt per flow: the cookie goes whatever the outcome.
	s.setFlowCookie(w, r, "", -1)
	flow, ok := s.readFlow(r)
	q := r.URL.Query()
	if !ok || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(flow.State)) != 1 {
		s.oidcFailed(w, r, http.StatusBadRequest, flow.Next, "oidc.error.expired")
		return
	}
	if e := q.Get("error"); e != "" {
		s.log.InfoContext(ctx, "oidc: provider refused", slog.String("error", e), slog.String("description", q.Get("error_description")))
		s.oidcFailed(w, r, http.StatusUnauthorized, flow.Next, "oidc.error.failed", s.cfg.OIDC.Name)
		return
	}

	attempts := []attempt{{s.ipLimit, clientIP(r, s.cfg.TrustedProxies).String()}}
	if !allowAll(attempts) {
		s.oidcFailed(w, r, http.StatusTooManyRequests, flow.Next, "signin.error.tooMany")
		return
	}
	id, err := s.sso.Exchange(ctx, s.oidcRedirectURL(r), flow.Flow, q.Get("code"))
	if errors.Is(err, sso.ErrUnavailable) {
		s.log.WarnContext(ctx, "oidc: callback", slog.Any("err", err))
		s.oidcFailed(w, r, http.StatusServiceUnavailable, flow.Next, "oidc.error.unavailable", s.cfg.OIDC.Name)
		return
	}
	if err != nil {
		s.log.WarnContext(ctx, "oidc: callback", slog.Any("err", err))
		s.oidcFailed(w, r, http.StatusUnauthorized, flow.Next, "oidc.error.failed", s.cfg.OIDC.Name)
		return
	}
	ext := auth.ExternalIdentity{Issuer: id.Issuer, Subject: id.Subject}
	if flow.Link != "" {
		refundAll(attempts)
		s.finishLink(w, r, flow, ext)
		return
	}
	token, err := s.auth.SignInExternal(ctx, ext, r.UserAgent())
	if key, args, ok := s.oidcRefusal(err); ok {
		s.oidcFailed(w, r, http.StatusForbidden, flow.Next, key, args...)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	refundAll(attempts)
	s.endCurrentSession(r)
	s.auth.SetSessionCookie(w, r, token)
	seeOther(w, r, auth.SafeNext(flow.Next))
}

// finishLink links ext to the signed-in user whose session started flow.
func (s *Server) finishLink(w http.ResponseWriter, r *http.Request, flow oidcFlow, ext auth.ExternalIdentity) {
	ctx := r.Context()
	id, ok := auth.FromContext(ctx)
	if !ok || subtle.ConstantTimeCompare([]byte(linkBinding(id)), []byte(flow.Link)) != 1 {
		// Signed out or in as someone else since the link started.
		s.oidcFailed(w, r, http.StatusForbidden, flow.Next, "oidc.error.linkSession", s.cfg.OIDC.Name)
		return
	}
	err := s.auth.Link(ctx, id, ext)
	if key, args, ok := s.oidcRefusal(err); ok {
		s.oidcFailed(w, r, http.StatusConflict, flow.Next, key, args...)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, "/settings/account?done=linked")
}

// oidcRefusal maps a refused SSO sign-in or link to its message and
// arguments.
func (s *Server) oidcRefusal(err error) (key i18n.Key, args []any, ok bool) {
	name := []any{s.cfg.OIDC.Name}
	switch {
	case errors.Is(err, auth.ErrNoAccount):
		return "oidc.error.notLinked", name, true
	case errors.Is(err, auth.ErrIdentityLinkedElsewhere):
		return "oidc.error.linkedElsewhere", name, true
	case errors.Is(err, auth.ErrOtherIdentityLinked):
		return "oidc.error.otherLinked", name, true
	}
	return key, nil, false
}

// oidcFailed shows the message: on the account page to a signed-in user
// (linking), otherwise on the sign-in page, password form included.
func (s *Server) oidcFailed(w http.ResponseWriter, r *http.Request, status int, next string, key i18n.Key, args ...any) {
	if id, ok := auth.FromContext(r.Context()); ok {
		profile := ui.Form{Values: map[string]string{"name": id.Name, "email": id.Email, "avatar_color": id.AvatarColor}}
		profile.Alert = i18n.T(r.Context(), key, args...)
		s.renderAccount(w, r, status, profile, ui.Form{}, "")
		return
	}
	v := s.signInView(next)
	v.Form.Alert = i18n.T(r.Context(), key, args...)
	s.renderSignIn(w, r, status, v)
}

func (s *Server) unlinkSSO(w http.ResponseWriter, r *http.Request) {
	if s.sso == nil {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	id := identity(r)
	err := s.auth.Unlink(ctx, id, s.sso.Issuer())
	switch {
	case errors.Is(err, auth.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, auth.ErrNoPassword):
		profile := ui.Form{Values: map[string]string{"name": id.Name, "email": id.Email, "avatar_color": id.AvatarColor}}
		s.renderAccount(w, r, http.StatusConflict, profile, ui.Form{}, i18n.T(ctx, "account.sso.needsPassword"))
	case err != nil:
		s.serverError(w, r, err)
	default:
		seeOther(w, r, "/settings/account?done=unlinked")
	}
}
