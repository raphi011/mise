package web

import (
	"context"
	"errors"
	"log/slog"
	netmail "net/mail"
	"strings"
	"time"

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
	if v.Ignore == "" {
		v.Ignore = i18n.T(ctx, "email.ignore")
	}
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

// emailChangedEmail tells the old address that the account's email
// address changed, in the account's language.
func emailChangedEmail(ctx context.Context, id *auth.Identity, change auth.EmailChange, link string) (mail.Message, error) {
	ctx = i18n.WithLocale(ctx, i18n.Match(id.Language, id.HouseholdLanguage))
	body := i18n.T(ctx, "email.emailChanged.body", id.Username, change.New)
	if change.New == "" {
		body = i18n.T(ctx, "email.emailChanged.bodyRemoved", id.Username)
	}
	return composeEmail(ctx, netmail.Address{Name: id.Name, Address: change.Old}, ui.EmailView{
		Title:    i18n.T(ctx, "email.emailChanged.subject"),
		Greeting: i18n.T(ctx, "email.reset.greeting", id.Name),
		Body:     body,
		Button:   i18n.T(ctx, "email.emailChanged.button"),
		Link:     link,
		Note:     i18n.T(ctx, "email.emailChanged.note"),
		Ignore:   i18n.T(ctx, "email.emailChanged.ignore"),
	})
}

// sendEmailChanged tells the old address about an email change, after the
// response, so a slow SMTP server does not hold up the save.
func (s *Server) sendEmailChanged(id *auth.Identity, change auth.EmailChange) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), mail.Timeout)
		defer cancel()
		link, err := s.absoluteURL("/settings/account")
		if err == nil {
			var msg mail.Message
			if msg, err = emailChangedEmail(ctx, id, change, link); err == nil {
				err = s.mailer.Send(ctx, msg)
			}
		}
		if err != nil {
			s.log.Error("send email change notice", slog.String("user", id.UserID.String()), slog.Any("err", err))
		}
	}()
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

// resetLookupTimeout bounds creating the reset links; the sends that follow
// get mail.Timeout each.
const resetLookupTimeout = 10 * time.Second

// sendResets creates and emails reset links for the accounts with email,
// after the response: the answer must not depend on whether one exists,
// and neither may its timing. Each send has its own deadline, so a slow
// SMTP server delays later recipients but does not drop their mail.
func (s *Server) sendResets(email string) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), resetLookupTimeout)
		tickets, err := s.auth.RequestPasswordReset(ctx, email)
		cancel()
		if err != nil {
			s.log.Error("password reset", slog.Any("err", err))
			return
		}
		for _, t := range tickets {
			if err := s.sendReset(t); err != nil {
				s.log.Error("send password reset", slog.String("user", t.UserID.String()), slog.Any("err", err))
			}
		}
	}()
}

// sendReset emails one reset link, within mail.Timeout.
func (s *Server) sendReset(t auth.ResetTicket) error {
	ctx, cancel := context.WithTimeout(context.Background(), mail.Timeout)
	defer cancel()
	link, err := s.absoluteURL("/reset/" + t.Token)
	if err != nil {
		return err
	}
	msg, err := resetEmail(ctx, t, link)
	if err != nil {
		return err
	}
	return s.mailer.Send(ctx, msg)
}
