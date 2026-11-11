package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/ui"
)

// newTokenCookie carries a just-created API token from the POST to the page
// that shows it once, like the invite link (post/redirect/get without
// storing the token anywhere readable).
const newTokenCookie = "mise_new_token"

const tokensPath = "/settings/account/tokens"

// tokenExpiries are the lifetimes offered for a new token, by form value.
var tokenExpiries = []struct {
	value string
	ttl   time.Duration
	label i18n.Key
}{
	{"never", 0, "tokens.expiry.never"},
	{"30d", 30 * 24 * time.Hour, "tokens.expiry.30d"},
	{"90d", 90 * 24 * time.Hour, "tokens.expiry.90d"},
	{"1y", 365 * 24 * time.Hour, "tokens.expiry.1y"},
}

func (s *Server) setTokenCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: newTokenCookie, Value: value, Path: tokensPath, MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request) {
	v := ui.TokensView{Form: ui.Form{Values: map[string]string{"scope": string(auth.ScopeRead), "expiry": "never"}}}
	if c, err := r.Cookie(newTokenCookie); err == nil && c.Value != "" {
		v.NewToken = c.Value
		s.setTokenCookie(w, r, "", -1)
	}
	s.renderTokens(w, r, http.StatusOK, v)
}

func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, status int, v ui.TokensView) {
	ctx := r.Context()
	id := identity(r)
	tokens, err := s.auth.Tokens(ctx, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := s.now()
	loc := location(id)
	for _, t := range tokens {
		row := ui.TokenRow{
			ID: t.ID.String(), Name: t.Name, Scope: i18n.T(ctx, scopeLabel(t.Scope)), Write: t.Scope == auth.ScopeWrite,
			Created:  i18n.T(ctx, "tokens.created", t.CreatedAt.In(loc).Format("2006-01-02")),
			LastUsed: i18n.T(ctx, "tokens.neverUsed"),
		}
		if t.LastUsedAt != nil {
			row.LastUsed = i18n.T(ctx, "tokens.lastUsed", t.LastUsedAt.In(loc).Format("2006-01-02"))
		}
		if t.ExpiresAt != nil {
			row.Expired = !now.Before(*t.ExpiresAt)
			row.Expires = i18n.T(ctx, "tokens.expires", t.ExpiresAt.In(loc).Format("2006-01-02"))
			if row.Expired {
				row.Expires = i18n.T(ctx, "tokens.expiredOn", t.ExpiresAt.In(loc).Format("2006-01-02"))
			}
		}
		v.Tokens = append(v.Tokens, row)
	}
	v.Scopes = []ui.Option{
		{Value: string(auth.ScopeRead), Label: i18n.T(ctx, "tokens.scope.read"), Hint: i18n.T(ctx, "tokens.scope.readHint")},
		{Value: string(auth.ScopeWrite), Label: i18n.T(ctx, "tokens.scope.write"), Hint: i18n.T(ctx, "tokens.scope.writeHint")},
	}
	for _, e := range tokenExpiries {
		v.Expiry = append(v.Expiry, ui.Option{Value: e.value, Label: i18n.T(ctx, e.label)})
	}
	if base := s.BaseURL(); base != nil {
		v.BaseURL = base.String()
	} else {
		v.BaseURL = requestOrigin(r, s.cfg.TrustedProxies)
	}
	if v.Notice == "" {
		v.Notice = noticeFor(r)
	}
	p := s.page(r, i18n.T(ctx, "tokens.pageTitle"), ui.NavSettings)
	p.BackHref, p.BackLabel, p.Settings = "/settings/account", i18n.T(ctx, "account.back"), "account"
	// The page may show a secret token.
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, ui.Tokens(p, v))
}

func scopeLabel(s auth.TokenScope) i18n.Key {
	if s == auth.ScopeWrite {
		return "tokens.scope.write"
	}
	return "tokens.scope.read"
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := formOf(r, "name", "scope", "expiry")
	var ttl time.Duration
	for _, e := range tokenExpiries {
		if e.value == f.Value("expiry") {
			ttl = e.ttl
		}
	}
	token, err := s.auth.CreateToken(ctx, identity(r), auth.NewTokenInput{
		Name: f.Value("name"), Scope: auth.TokenScope(f.Value("scope")), TTL: ttl,
	})
	if f, ok := withErrors(ctx, f, err); ok {
		s.renderTokens(w, r, http.StatusUnprocessableEntity, ui.TokensView{Form: f})
		return
	}
	if errors.Is(err, auth.ErrTooManyTokens) {
		f.Alert = i18n.T(ctx, "tokens.error.tooMany")
		s.renderTokens(w, r, http.StatusUnprocessableEntity, ui.TokensView{Form: f})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setTokenCookie(w, r, token, 120)
	seeOther(w, r, tokensPath)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	tokenID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	err = s.auth.RevokeToken(r.Context(), identity(r), tokenID)
	if errors.Is(err, auth.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seeOther(w, r, tokensPath+"?done=tokenRevoked")
}
