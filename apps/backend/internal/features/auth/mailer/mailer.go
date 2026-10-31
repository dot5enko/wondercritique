// Package mailer sends transactional email: via Resend when RESEND_API_KEY is set, otherwise (dev only) to the log.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/resend/resend-go/v2"

	"github.com/dot5enko/wondercritique/apps/backend/internal/config"
)

// Email is a single outgoing message.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer delivers email.
type Mailer interface {
	Send(ctx context.Context, e Email) error
}

// New returns a ResendMailer when RESEND_API_KEY is set (MAIL_FROM is then required), otherwise a LogMailer.
// The LogMailer prints message bodies (login codes included), so it is refused outside dev.
func New(cfg *config.Config, log *slog.Logger) (Mailer, error) {
	if cfg.ResendAPIKey != "" {
		if cfg.MailFrom == "" {
			return nil, errors.New("mailer: MAIL_FROM is required when RESEND_API_KEY is set")
		}
		return NewResend(cfg.ResendAPIKey, cfg.MailFrom), nil
	}
	if !cfg.IsDev() {
		return nil, fmt.Errorf("mailer: RESEND_API_KEY is required when APP_ENV=%s (the log mailer is dev-only)", cfg.Env)
	}
	log.Warn("mailer: RESEND_API_KEY not set, emails (including login codes) are written to the log")
	return NewLog(log), nil
}

// ResendMailer sends email through the Resend API.
type ResendMailer struct {
	client *resend.Client
	from   string
}

func NewResend(apiKey, from string) *ResendMailer {
	return &ResendMailer{client: resend.NewClient(apiKey), from: from}
}

func (m *ResendMailer) Send(ctx context.Context, e Email) error {
	_, err := m.client.Emails.SendWithContext(ctx, &resend.SendEmailRequest{
		From:    m.from,
		To:      []string{e.To},
		Subject: e.Subject,
		Text:    e.Text,
		Html:    e.HTML,
	})
	if err != nil {
		return fmt.Errorf("mailer: resend: %w", err)
	}
	return nil
}

// LogMailer writes emails to the log at info level. Dev only.
type LogMailer struct{ log *slog.Logger }

func NewLog(log *slog.Logger) *LogMailer { return &LogMailer{log: log.With("component", "mailer")} }

func (m *LogMailer) Send(ctx context.Context, e Email) error {
	m.log.InfoContext(ctx, "email (not sent, dev log mailer)", "to", e.To, "subject", e.Subject, "text", e.Text)
	return nil
}
