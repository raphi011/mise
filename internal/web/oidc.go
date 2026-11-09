package web

import (
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
const (
	oidcCookie    = "mise_oidc"
	oidcCookieAge = 10 * time.Minute
)

// oidcFlow is the flow cookie's content.
type oidcFlow struct {
	sso.Flow
	Next    string `json:"next,omitempty"`
	Started int64  `json:"t"`
}

// ssoAvailable reports whether the sign-in page offers single sign-on:
// it is configured and the base URL for the callback is known.
func (s *Server) ssoAvailable() bool { return s.sso != nil && s.BaseURL() != nil }

func (s *Server) oidcRedirectURL() string { return s.BaseURL().JoinPath(oidcCallbackPath).String() }

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
	ctx := r.Context()
	flow := oidcFlow{Flow: sso.NewFlow(), Next: auth.SafeNext(r.PostFormValue("next")), Started: s.now().Unix()}
	target, err := s.sso.AuthURL(ctx, s.oidcRedirectURL(), flow.Flow)
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
	id, err := s.sso.Exchange(ctx, s.oidcRedirectURL(), flow.Flow, q.Get("code"))
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
	token, err := s.auth.SignInExternal(ctx, auth.ExternalIdentity{
		Issuer: id.Issuer, Subject: id.Subject, Email: id.Email, EmailVerified: id.EmailVerified,
	}, r.UserAgent())
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

// oidcRefusal maps a refused SSO sign-in to its message and arguments.
func (s *Server) oidcRefusal(err error) (key i18n.Key, args []any, ok bool) {
	name := []any{s.cfg.OIDC.Name}
	switch {
	case errors.Is(err, auth.ErrNoAccount):
		return "oidc.error.noAccount", nil, true
	case errors.Is(err, auth.ErrAmbiguousEmail):
		return "oidc.error.ambiguous", nil, true
	case errors.Is(err, auth.ErrEmailUnverified):
		return "oidc.error.unverified", name, true
	case errors.Is(err, auth.ErrOtherIdentityLinked):
		return "oidc.error.otherLinked", name, true
	}
	return key, nil, false
}

// oidcFailed shows the sign-in page, password form included, with the
// message.
func (s *Server) oidcFailed(w http.ResponseWriter, r *http.Request, status int, next string, key i18n.Key, args ...any) {
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
