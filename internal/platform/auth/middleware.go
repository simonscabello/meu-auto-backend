package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
	"github.com/meu-auto/meu-auto-backend/internal/platform/httpx"
)

type userIDCtxKey struct{}

// Middleware rejects any request without a valid access token and puts the caller's id
// into the context.
//
// It answers with the same message whether the header is missing, malformed, expired or
// forged. Distinguishing them tells an attacker which part of their guess was right.
func Middleware(tokens *TokenService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				httpx.Error(w, r, apperr.Unauthorized(
					"Autenticação necessária."))
				return
			}

			userID, err := tokens.ParseAccessToken(raw)
			if err != nil {
				// The cause is wrapped for the log; the client only ever sees the message.
				httpx.Error(w, r, apperr.Wrap(err, apperr.CodeUnauthorized,
					"Sessão inválida ou expirada."))
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), userID)))
		})
	}
}

// WithUserID attaches an authenticated user id to a context. Exported for tests.
func WithUserID(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDCtxKey{}, userID)
}

// UserID returns the authenticated caller's id.
//
// The second return is false outside an authenticated route. A handler mounted behind
// Middleware can treat that as a wiring bug rather than a runtime condition.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	userID, ok := ctx.Value(userIDCtxKey{}).(uuid.UUID)
	return userID, ok && userID != uuid.Nil
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	if token = strings.TrimSpace(token); token == "" {
		return "", false
	}
	return token, true
}
