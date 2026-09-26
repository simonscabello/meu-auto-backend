package notification

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Outside the sending hour the job must not touch anything at all. The repository here is
// nil: reaching it would panic, which is exactly the regression this guards against — a
// job that works through the night because the hour check moved.
func TestRemindersSleepOutsideTheSendingHour(t *testing.T) {
	saoPaulo, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	for _, clock := range []string{"08:59", "10:00", "21:00", "03:00"} {
		t.Run(clock, func(t *testing.T) {
			at, err := time.ParseInLocation("2006-01-02 15:04", "2026-09-26 "+clock, saoPaulo)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			r := &Reminders{
				hour:     9,
				location: saoPaulo,
				log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
				// A UTC clock on purpose: the hour that counts is São Paulo's, not the
				// server's.
				now: func() time.Time { return at.UTC() },
			}
			r.tick(context.Background())
		})
	}
}
