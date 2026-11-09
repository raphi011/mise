package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/netip"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

// formOf collects the named fields of a parsed form for re-rendering.
func formOf(r *http.Request, names ...string) ui.Form {
	f := ui.Form{Values: map[string]string{}, Errors: map[string]string{}}
	for _, n := range names {
		f.Values[n] = r.PostFormValue(n)
	}
	return f
}

// withErrors translates validation errors into f. It reports false when err
// is not a validation error.
func withErrors(ctx context.Context, f ui.Form, err error) (ui.Form, bool) {
	var fe auth.FieldErrors
	if !errors.As(err, &fe) {
		return f, false
	}
	if f.Errors == nil {
		f.Errors = map[string]string{}
	}
	for field, key := range fe {
		f.Errors[field] = i18n.T(ctx, key)
	}
	return f, true
}

// requestOrigin guesses the public origin from the request, for prefilling
// the base URL at setup. X-Forwarded-Proto counts only from trusted proxies.
func requestOrigin(r *http.Request, trusted []netip.Prefix) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil &&
		isTrusted(peer.Addr().Unmap(), trusted) && r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- Setup ----

var setupFields = []string{"household", "timezone", "week_start", "base_url", "name", "username", "email"}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	need, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !need {
		http.Redirect(w, r, "/signin", http.StatusFound)
		return
	}
	f := ui.Form{Values: map[string]string{"week_start": "mon", "base_url": requestOrigin(r, s.cfg.TrustedProxies)}}
	if s.cfg.BaseURL != nil {
		f.Values["base_url"] = s.cfg.BaseURL.String()
	}
	s.renderSetup(w, r, http.StatusOK, f)
}

func (s *Server) renderSetup(w http.ResponseWriter, r *http.Request, status int, f ui.Form) {
	p := ui.Page{Title: i18n.T(r.Context(), "setup.title")}
	s.render(w, r, status, ui.Setup(p, ui.SetupView{Form: f, BaseURLFixed: s.cfg.BaseURL != nil}))
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := formOf(r, setupFields...)
	in := auth.SetupInput{
		HouseholdName: f.Value("household"),
		Timezone:      f.Value("timezone"),
		WeekStart:     f.Value("week_start"),
		Language:      i18n.Locale(ctx).String(),
		BaseURL:       f.Value("base_url"),
		Name:          f.Value("name"),
		Username:      f.Value("username"),
		Email:         f.Value("email"),
		Password:      r.PostFormValue("password"),
		UserAgent:     r.UserAgent(),
	}
	if s.cfg.BaseURL != nil {
		in.BaseURL = s.cfg.BaseURL.String()
		f.Values["base_url"] = in.BaseURL
	}
	res, err := s.auth.Setup(ctx, in)
	if errors.Is(err, auth.ErrSetupDone) {
		seeOther(w, r, "/signin")
		return
	}
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderSetup(w, r, http.StatusUnprocessableEntity, f)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.cfg.BaseURL == nil {
		if err := s.setBaseURL(res.BaseURL); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.auth.SetSessionCookie(w, r, res.Token)
	seeOther(w, r, "/")
}

// ---- Sign in / out ----

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.FromContext(r.Context()); ok {
		http.Redirect(w, r, auth.SafeNext(r.URL.Query().Get("next")), http.StatusFound)
		return
	}
	v := ui.SignInView{Next: r.URL.Query().Get("next")}
	s.renderSignIn(w, r, http.StatusOK, v)
}

func (s *Server) renderSignIn(w http.ResponseWriter, r *http.Request, status int, v ui.SignInView) {
	p := ui.Page{Title: i18n.T(r.Context(), "signin.title")}
	s.render(w, r, status, ui.SignIn(p, v))
}

func (s *Server) signInSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	username, password := r.PostFormValue("username"), r.PostFormValue("password")
	v := ui.SignInView{Form: formOf(r, "username"), Next: r.PostFormValue("next")}
	invalid := func() {
		v.Form.Alert = i18n.T(ctx, "signin.error.invalid")
		s.renderSignIn(w, r, http.StatusUnauthorized, v)
	}
	// Input no account can match needs no attempt and no limiter key.
	if !auth.PlausibleCredentials(username, password) {
		invalid()
		return
	}

	ip, key := clientIP(r, s.cfg.TrustedProxies).String(), auth.UsernameKey(username)
	attempts := []attempt{{s.ipLimit, ip}, {s.pairLimit, ip + "\x00" + key}, {s.userLimit, key}}
	if !allowAll(attempts) {
		v.Form.Alert = i18n.T(ctx, "signin.error.tooMany")
		s.renderSignIn(w, r, http.StatusTooManyRequests, v)
		return
	}

	token, err := s.auth.SignIn(ctx, username, password, r.UserAgent())
	if errors.Is(err, auth.ErrInvalidCredentials) {
		invalid()
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	refundAll(attempts)
	s.endCurrentSession(r)
	s.auth.SetSessionCookie(w, r, token)
	seeOther(w, r, auth.SafeNext(v.Next))
}

// attempt is one rate-limiter bucket an action spends from.
type attempt struct {
	limiter *auth.Limiter
	key     string
}

// allowAll spends one attempt from every bucket, or from none: when a bucket
// is empty, the attempts already spent are given back.
func allowAll(attempts []attempt) bool {
	for i, a := range attempts {
		if !a.limiter.Allow(a.key) {
			refundAll(attempts[:i])
			return false
		}
	}
	return true
}

// refundAll gives back one attempt to every bucket.
func refundAll(attempts []attempt) {
	for _, a := range attempts {
		a.limiter.Refund(a.key)
	}
}

// endCurrentSession deletes the session the request came with, if any, when
// a new one replaces it.
func (s *Server) endCurrentSession(r *http.Request) {
	if id, ok := auth.FromContext(r.Context()); ok {
		if err := s.auth.SignOut(r.Context(), id.SessionID); err != nil {
			s.log.WarnContext(r.Context(), "end replaced session", "err", err)
		}
	}
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := s.auth.SignOut(r.Context(), id.SessionID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.auth.ClearSessionCookie(w, r)
	seeOther(w, r, "/signin")
}

func (s *Server) forgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	p := ui.Page{Title: i18n.T(r.Context(), "forgot.title")}
	s.render(w, r, http.StatusOK, ui.ForgotPassword(p))
}

// ---- Accept invite ----

func (s *Server) inviteView(ctx context.Context, token string) (ui.InviteView, auth.InviteInfo, error) {
	info, err := s.auth.InviteByToken(ctx, token)
	if err != nil {
		return ui.InviteView{}, info, err
	}
	days := int(math.Ceil(info.ExpiresAt.Sub(s.auth.Now()).Hours() / 24))
	expires := i18n.T(ctx, "accept.expiresDays", days)
	if days <= 1 {
		expires = i18n.T(ctx, "accept.expiresSoon")
	}
	return ui.InviteView{
		Token:         token,
		HouseholdName: info.HouseholdName,
		InviterName:   info.InviterName,
		InviterColor:  info.InviterColor,
		Expires:       expires,
	}, info, nil
}

func (s *Server) renderInviteInvalid(w http.ResponseWriter, r *http.Request) {
	p := ui.Page{Title: i18n.T(r.Context(), "accept.invalid.title")}
	s.render(w, r, http.StatusNotFound, ui.InviteInvalid(p))
}

func (s *Server) acceptInvitePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Invite links carry a secret; don't leak it to linked pages.
	w.Header().Set("Referrer-Policy", "no-referrer")
	v, info, err := s.inviteView(ctx, r.PathValue("token"))
	if errors.Is(err, auth.ErrInviteInvalid) {
		s.renderInviteInvalid(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := ui.Form{Values: map[string]string{"email": info.Email}}
	p := ui.Page{Title: i18n.T(ctx, "accept.title")}
	s.render(w, r, http.StatusOK, ui.AcceptInvite(p, v, f))
}

func (s *Server) acceptInviteSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	f := formOf(r, "name", "username", "email")
	session, err := s.auth.AcceptInvite(ctx, token, auth.AcceptInput{
		Name:      f.Value("name"),
		Username:  f.Value("username"),
		Email:     f.Value("email"),
		Password:  r.PostFormValue("password"),
		UserAgent: r.UserAgent(),
	})
	if errors.Is(err, auth.ErrInviteInvalid) {
		s.renderInviteInvalid(w, r)
		return
	}
	if f, ok := withErrors(ctx, f, err); ok {
		v, _, err := s.inviteView(ctx, token)
		if err != nil {
			s.renderInviteInvalid(w, r)
			return
		}
		p := ui.Page{Title: i18n.T(ctx, "accept.title")}
		s.render(w, r, http.StatusUnprocessableEntity, ui.AcceptInvite(p, v, f))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.endCurrentSession(r)
	s.auth.SetSessionCookie(w, r, session)
	seeOther(w, r, "/")
}
