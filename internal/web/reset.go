package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

func (s *Server) forgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	v := ui.ForgotView{CanEmail: s.mailer != nil, Sent: s.mailer != nil && r.URL.Query().Get("sent") == "1"}
	s.renderForgot(w, r, http.StatusOK, v)
}

func (s *Server) renderForgot(w http.ResponseWriter, r *http.Request, status int, v ui.ForgotView) {
	p := ui.Page{Title: i18n.T(r.Context(), "forgot.title")}
	s.render(w, r, status, ui.ForgotPassword(p, v))
}

// forgotPasswordSubmit asks for reset links for an email address. It
// answers the same whether or not an account has the address; links are
// created and sent in the background.
func (s *Server) forgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if s.mailer == nil {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	f := formOf(r, "email")
	v := ui.ForgotView{CanEmail: true, Form: f}
	email := strings.TrimSpace(f.Value("email"))
	if !auth.ValidEmail(email) {
		key := i18n.Key("form.error.email")
		if email == "" {
			key = "form.error.required"
		}
		v.Form.Errors = map[string]string{"email": i18n.T(ctx, key)}
		s.renderForgot(w, r, http.StatusUnprocessableEntity, v)
		return
	}
	ip := clientIP(r, s.cfg.TrustedProxies).String()
	if !allowAll([]attempt{{s.resetIPLimit, ip}, {s.resetAddrLimit, strings.ToLower(email)}}) {
		v.Form.Alert = i18n.T(ctx, "signin.error.tooMany")
		s.renderForgot(w, r, http.StatusTooManyRequests, v)
		return
	}
	s.sendResets(email)
	seeOther(w, r, "/forgot-password?sent=1")
}

// resetHeaders keep the token in the URL out of caches and referrers.
func resetHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

func (s *Server) resetPage(w http.ResponseWriter, r *http.Request) {
	resetHeaders(w)
	token := r.PathValue("token")
	info, err := s.auth.ResetByToken(r.Context(), token)
	if errors.Is(err, auth.ErrResetInvalid) {
		s.renderResetInvalid(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderReset(w, r, http.StatusOK, ui.ResetView{Token: token, Username: info.Username}, ui.Form{})
}

func (s *Server) renderReset(w http.ResponseWriter, r *http.Request, status int, v ui.ResetView, f ui.Form) {
	p := ui.Page{Title: i18n.T(r.Context(), "reset.title")}
	s.render(w, r, status, ui.ResetPassword(p, v, f))
}

func (s *Server) renderResetInvalid(w http.ResponseWriter, r *http.Request) {
	p := ui.Page{Title: i18n.T(r.Context(), "reset.invalid.title")}
	s.render(w, r, http.StatusGone, ui.ResetInvalid(p))
}

// resetSubmit sets the new password, ends the account's sessions and
// signs in on this device.
func (s *Server) resetSubmit(w http.ResponseWriter, r *http.Request) {
	resetHeaders(w)
	ctx := r.Context()
	token := r.PathValue("token")
	if !s.resetSubmitLimit.Allow(clientIP(r, s.cfg.TrustedProxies).String()) {
		info, err := s.auth.ResetByToken(ctx, token)
		if err != nil {
			s.renderResetInvalid(w, r)
			return
		}
		f := ui.Form{Alert: i18n.T(ctx, "signin.error.tooMany")}
		s.renderReset(w, r, http.StatusTooManyRequests, ui.ResetView{Token: token, Username: info.Username}, f)
		return
	}
	session, userID, err := s.auth.CompleteReset(ctx, token, r.PostFormValue("password"), r.UserAgent())
	if errors.Is(err, auth.ErrResetInvalid) {
		s.renderResetInvalid(w, r)
		return
	}
	if f, ok := withErrors(ctx, ui.Form{}, err); ok {
		info, err := s.auth.ResetByToken(ctx, token)
		if err != nil {
			s.renderResetInvalid(w, r)
			return
		}
		s.renderReset(w, r, http.StatusUnprocessableEntity, ui.ResetView{Token: token, Username: info.Username}, f)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.sessionsEnded(userID)
	s.endCurrentSession(r)
	s.auth.SetSessionCookie(w, r, session)
	seeOther(w, r, "/")
}
