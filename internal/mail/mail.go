// Package mail sends the few emails mise writes: invites and password
// resets. Without SMTP configuration there is no Mailer and callers fall
// back to links an admin hands on.
package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/raphi011/mise/internal/config"
)

// Timeout bounds connecting to and talking with the SMTP server.
const Timeout = 15 * time.Second

// Message is one email with a plain-text and an HTML body.
type Message struct {
	// To is the recipient, an address or "Name <address>".
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer sends messages.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends through the configured SMTP server, one connection per
// message.
type SMTP struct {
	cfg config.SMTP
}

// NewSMTP returns a Mailer for cfg, which must be enabled.
func NewSMTP(cfg config.SMTP) *SMTP { return &SMTP{cfg: cfg} }

// Send delivers m as multipart/alternative, text first.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	opts := []gomail.Option{gomail.WithPort(s.cfg.Port), gomail.WithTimeout(Timeout)}
	switch s.cfg.TLS {
	case config.TLSImplicit:
		opts = append(opts, gomail.WithSSL())
	case config.TLSNone:
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if s.cfg.SkipVerify {
		opts = append(opts, gomail.WithTLSConfig(&tls.Config{
			ServerName:         s.cfg.Host,
			InsecureSkipVerify: true, //nolint:gosec // opted into for relays with self-signed certificates
			MinVersion:         tls.VersionTLS12,
		}))
	}
	if s.cfg.Username != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
			gomail.WithUsername(s.cfg.Username), gomail.WithPassword(s.cfg.Password))
	}
	client, err := gomail.NewClient(s.cfg.Host, opts...)
	if err != nil {
		return fmt.Errorf("mail: client: %w", err)
	}
	msg := gomail.NewMsg()
	if err := msg.From(s.cfg.From.String()); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	if err := msg.To(m.To); err != nil {
		return fmt.Errorf("mail: to: %w", err)
	}
	msg.Subject(m.Subject)
	msg.SetDate()
	msg.SetMessageID()
	msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	return nil
}
