// Package mailer sends the few transactional e-mails the product needs.
//
// The interface is deliberately narrow: one method per e-mail the product actually sends,
// not a generic Send(subject, body). A generic mailer invites templates, layouts and a
// queue — none of which anything needs yet.
package mailer

import (
	"context"
	"log/slog"
)

// Mailer sends transactional e-mail.
type Mailer interface {
	SendPasswordReset(ctx context.Context, to, name, resetURL string) error
}

// LogMailer writes the e-mail to the log instead of sending it.
//
// Development only. config.Load refuses to start a production process without a real
// provider configured, because a password reset link in the logs is account takeover for
// anyone who can read them.
type LogMailer struct {
	Log *slog.Logger
}

func (m LogMailer) SendPasswordReset(_ context.Context, to, name, resetURL string) error {
	m.Log.Info("password reset e-mail (not sent — LogMailer)",
		slog.String("to", to),
		slog.String("name", name),
		slog.String("reset_url", resetURL))
	return nil
}
