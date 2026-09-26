package push

import (
	"context"
	"log/slog"
)

// Log writes each message instead of sending it (NOTIFICATIONS_DEBUG=true).
//
// It is how the text and the recipients of a reminder are checked with no Firebase
// account at all, locally or against a copy of production.
type Log struct {
	Logger *slog.Logger
}

func (l Log) Send(_ context.Context, token string, msg Message) error {
	l.Logger.Info("push not sent (NOTIFICATIONS_DEBUG)",
		slog.String("token", shortToken(token)),
		slog.String("title", msg.Title),
		slog.String("body", msg.Body),
		slog.Any("data", msg.Data))
	return nil
}

// shortToken is enough of a token to tell two phones apart in a log line, and no more.
func shortToken(token string) string {
	const keep = 8
	if len(token) <= keep {
		return token
	}
	return token[:keep] + "…"
}
