package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/logging"
)

const requestIDHeader = "X-Request-Id"

// maxClientRequestIDLength bounds an inbound request id. The value ends up in log lines,
// so it is treated as untrusted input, not as a trusted identifier.
const maxClientRequestIDLength = 64

type requestIDCtxKey struct{}

// RequestID returns the id assigned to the current request, or "" outside one.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDCtxKey{}).(string)
	return id
}

// WithRequestID attaches a request id to a context. Exported for tests.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDCtxKey{}, id)
}

// RequestIDMiddleware assigns every request an id, echoes it back in the response, and
// puts a logger tagged with it into the context.
//
// The app may supply its own id so one user-visible failure can be traced end to end.
// That value is attacker-controllable, so it is accepted only if it is short and free of
// characters that could forge a second entry in a log line.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(requestIDHeader))
		if id == "" {
			if generated, err := uuid.NewV7(); err == nil {
				id = generated.String()
			} else {
				id = uuid.NewString()
			}
		}

		w.Header().Set(requestIDHeader, id)

		ctx := WithRequestID(r.Context(), id)
		ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With(
			slog.String("request_id", id)))

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sanitizeRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxClientRequestIDLength {
		return ""
	}
	for _, r := range raw {
		isSafe := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !isSafe {
			return ""
		}
	}
	return raw
}

// statusRecorder captures the status code so the logger can report it after the handler
// has returned.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (sr *statusRecorder) WriteHeader(status int) {
	if sr.status == 0 {
		sr.status = status
	}
	sr.ResponseWriter.WriteHeader(status)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	n, err := sr.ResponseWriter.Write(b)
	sr.bytes += n
	return n, err
}

// LoggerMiddleware logs one structured line per request after it completes.
//
// The query string is deliberately not logged: it is the easiest place for a token or a
// personal identifier to end up in a log aggregator.
func LoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		logging.FromContext(r.Context()).Info("http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", time.Since(start)))
	})
}

// RecovererMiddleware turns a panic into a logged 500 instead of a dropped connection.
//
// A panic is always a bug, so the stack is logged in full — but the client still gets
// only the generic internal message, with the request id to quote to support.
func RecovererMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// The client hung up mid-write; there is no response left to salvage.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			logging.FromContext(r.Context()).Error("panic recovered",
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())))

			Error(w, r, apperr.New(apperr.CodeInternal,
				"Ocorreu um erro inesperado. Tente novamente."))
		}()

		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware answers preflight requests and echoes an allowed origin.
//
// Origins come from configuration, and config.Load refuses to start a production process
// with a wildcard, so the permissive branch can only be reached in development.
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAny := slices.Contains(allowedOrigins, "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if origin != "" && (allowAny || slices.Contains(allowedOrigins, origin)) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods",
					"GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, "+requestIDHeader)
				w.Header().Set("Access-Control-Expose-Headers", requestIDHeader)
				w.Header().Set("Access-Control-Max-Age", "300")
				// Caches must not serve one origin's response to another.
				w.Header().Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
