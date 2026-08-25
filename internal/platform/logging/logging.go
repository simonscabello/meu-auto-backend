// Package logging builds the process logger and carries a request-scoped logger through
// the context.
//
// Output is JSON on stdout: Railway reads stdout natively, and so does a systemd unit or
// a Docker log driver on a VPS. There is no APM, no tracing and no metrics exporter —
// they get added when a real question shows up that structured logs cannot answer
// (SPEC.md D-08).
//
// Never log a password, a token, a hash, a full e-mail address or a CPF.
package logging

import (
	"context"
	"log/slog"
	"os"
)

type ctxKey struct{}

// New builds the process-wide logger.
func New(level slog.Level, appEnv string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		// Source location costs a stack walk per record and is only worth it while
		// debugging locally.
		AddSource: level == slog.LevelDebug,
	})
	return slog.New(handler).With(slog.String("env", appEnv))
}

// WithLogger returns a context carrying the given logger.
//
// The HTTP middleware puts a logger already tagged with the request id here, so any code
// below it logs with the request id attached without having to thread it manually.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext returns the request-scoped logger, falling back to the default logger so
// callers never have to nil-check.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}
