package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	// Short enough that a leaked access token expires before it is worth much, long
	// enough that the app is not refreshing on every screen.
	AccessTokenTTL = 15 * time.Minute

	// The app should not ask the user to sign in again every month of active use; the
	// rotation on every refresh is what actually bounds a stolen token's life.
	RefreshTokenTTL = 30 * 24 * time.Hour

	// Password reset links expire fast: the window in which a forwarded or intercepted
	// e-mail is still useful should be minutes, not days.
	PasswordResetTTL = time.Hour

	opaqueTokenBytes = 32
)

// ErrInvalidToken means the token is absent, malformed, expired, or not signed by us.
// It never distinguishes between those: telling an attacker which one it is helps them.
var ErrInvalidToken = errors.New("auth: invalid token")

// TokenService issues and validates access tokens, and mints opaque refresh tokens.
type TokenService struct {
	secret []byte
	issuer string
}

// NewTokenService builds the service. The secret comes from JWT_SECRET, which config
// refuses to accept below 32 characters.
func NewTokenService(secret []byte, issuer string) *TokenService {
	return &TokenService{secret: secret, issuer: issuer}
}

// IssueAccessToken returns a signed HS256 JWT for the user and its expiry.
//
// Claims are deliberately minimal — subject, issuer, timestamps. Anything else (name,
// e-mail, roles) would be a copy of state that can go stale inside a token nobody can
// revoke.
func (s *TokenService) IssueAccessToken(userID uuid.UUID, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(AccessTokenTTL)

	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		Issuer:    s.issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// ParseAccessToken validates a token and returns the user id it names.
func (s *TokenService) ParseAccessToken(raw string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims

	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return s.secret, nil },
		// Pinning the algorithm is what stops the classic attack of handing the server a
		// token with alg "none", or an HS256 token signed with a public key.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: subject is not a uuid", ErrInvalidToken)
	}
	return userID, nil
}

// NewOpaqueToken mints a random bearer value, returning the string to hand the client and
// the SHA-256 digest to store. Used for refresh tokens and password reset tokens alike.
//
// The plaintext is never persisted: a dump of refresh_tokens or password_reset_tokens must
// not let anyone mint a session or take over an account. SHA-256 rather than argon2id is
// correct here — this is a 256-bit random value, not a human-chosen password, so there is
// no dictionary to slow down.
func NewOpaqueToken() (plain string, hash []byte, err error) {
	raw := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("auth: read token: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(raw)
	return plain, HashOpaqueToken(plain), nil
}

// HashOpaqueToken returns the digest stored for an opaque token.
func HashOpaqueToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
