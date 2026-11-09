package web

import (
	"context"
	"errors"
	"log/slog"
	netmail "net/mail"
	"strings"

	"github.com/raphi011/mise/internal/auth"
	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/mail"
	"github.com/raphi011/mise/internal/ui"
)

// errNoBaseURL means a link cannot be built: no public URL is known yet.
var errNoBaseURL = errors.New("web: no base URL for email links")

// absoluteURL returns path on the public base URL.
func (s *Server) absoluteURL(path string) (string, error) {
	base := s.BaseURL()
	if base == nil {
		return "", errNoBaseURL
	}
	return base.String() + path, nil
}

// composeEmail renders v as a message to to, in the language of ctx. The
// text part carries the same words as the HTML part.
func composeEmail(ctx context.Context, to netmail.Address, v ui.EmailView) (mail.Message, error) {
	v.Lang = i18n.Locale(ctx).String()
	v.Fallback = i18n.T(ctx, "email.fallback")
	v.Ignore = i18n.T(ctx, "email.ignore")
	var html strings.Builder
	if err := ui.Email(v).Render(ctx, &html); err != nil {
		return mail.Message{}, err
	}
	var text []string
	if v.Greeting != "" {
		text = append(text, v.Greeting)
	}
	text = append(text, v.Body, v.Link, v.Note, v.Ignore)
	return mail.Message{
		To: to.String(), Subject: v.Title,
		Text: strings.Join(text, "\n\n") + "\n", HTML: html.String(),
	}, nil
}

// inviteEmail is the email carrying an invite link, in the household's
// default language.
func inviteEmail(ctx context.Context, id *auth.Identity, to, link string) (mail.Message, error) {
	ctx = i18n.WithLocale(ctx, i18n.Match(id.HouseholdLanguage))
	return composeEmail(ctx, netmail.Address{Address: to}, ui.EmailView{
		Title:  i18n.T(ctx, "email.invite.subject", id.Name, id.HouseholdName),
		Body:   i18n.T(ctx, "email.invite.body", id.Name, id.HouseholdName),
		Button: i18n.T(ctx, "email.invite.button", id.HouseholdName),
		Link:   link,
		Note:   i18n.T(ctx, "email.invite.note"),
	})
}

// resetEmail is the email carrying a password-reset link, in the
// account's language.
func resetEmail(ctx context.Context, t auth.ResetTicket, link string) (mail.Message, error) {
	ctx = i18n.WithLocale(ctx, i18n.Match(t.Language))
	return composeEmail(ctx, netmail.Address{Name: t.Name, Address: t.Email}, ui.EmailView{
		Title:    i18n.T(ctx, "email.reset.subject"),
		Greeting: i18n.T(ctx, "email.reset.greeting", t.Name),
		Body:     i18n.T(ctx, "email.reset.body", t.Username),
		Button:   i18n.T(ctx, "email.reset.button"),
		Link:     link,
		Note:     i18n.T(ctx, "email.reset.note"),
	})
}

// sendInvite emails an invite link. The caller shows the link either way.
func (s *Server) sendInvite(ctx context.Context, id *auth.Identity, to, token string) error {
	link, err := s.absoluteURL("/invite/" + token)
	if err != nil {
		return err
	}
	msg, err := inviteEmail(ctx, id, to, link)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, mail.Timeout)
	defer cancel()
	return s.mailer.Send(ctx, msg)
}

// sendResets creates and emails reset links for the accounts with email,
// after the response: the answer must not depend on whether one exists,
// and neither may its timing.
func (s *Server) sendResets(email string) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*mail.Timeout)
		defer cancel()
		tickets, err := s.auth.RequestPasswordReset(ctx, email)
		if err != nil {
			s.log.ErrorContext(ctx, "password reset", slog.Any("err", err))
			return
		}
		for _, t := range tickets {
			link, err := s.absoluteURL("/reset/" + t.Token)
			if err == nil {
				var msg mail.Message
				if msg, err = resetEmail(ctx, t, link); err == nil {
					err = s.mailer.Send(ctx, msg)
				}
			}
			if err != nil {
				s.log.ErrorContext(ctx, "send password reset", slog.String("user", t.UserID.String()), slog.Any("err", err))
			}
		}
	}()
}
