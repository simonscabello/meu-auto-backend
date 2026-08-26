package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/identity/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/auth"
	"github.com/simonscabello/meu-auto-backend/internal/platform/mailer"
	"github.com/simonscabello/meu-auto-backend/internal/platform/ratelimit"
	"github.com/simonscabello/meu-auto-backend/internal/platform/validate"
)

// Rate limits.
//
// Two dimensions, with deliberately different budgets:
//
//   - By e-mail: tight. This is what protects one account from being guessed at, and one
//     person only ever generates attempts against their own address.
//   - By IP: loose. Brazilian mobile carriers put thousands of subscribers behind one
//     CGNAT address, so an IP budget sized like the e-mail one would lock out an entire
//     carrier the moment a handful of its users mistyped a password. It exists to catch
//     one host spraying many accounts, not to police an individual.
//
// Dropping the IP dimension entirely would let a botnet walk a leaked address list one
// attempt per account. Keeping it tight would take the product down for real users. Loose
// is the honest middle.
const (
	loginAttemptsPerEmail = 10
	loginAttemptsPerIP    = 60
	loginWindow           = 15 * time.Minute

	resetRequestsPerEmail = 5
	resetRequestsPerIP    = 20
	resetWindow           = time.Hour
)

// UserDataEraser removes data belonging to a user that lives outside this module.
//
// It exists because not everything a person owns hangs off the users row. Vehicles
// deliberately carry no user_id — the link is an ownership row (SPEC.md RN-07) — so a
// cascade cannot reach them, and account erasure would otherwise leave orphaned vehicles
// carrying a plate and a chassis after someone asked to be forgotten.
//
// Each module that owns user-scoped data registers one. Identity depends on the interface,
// never on the module, so the dependency arrow still points inwards.
type UserDataEraser interface {
	EraseUserData(ctx context.Context, userID uuid.UUID) error
}

// Session is everything a client needs after authenticating.
type Session struct {
	User             db.User
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Service holds the identity business rules. It is the only place in this module that
// decides what the client is told.
type Service struct {
	repo   *Repository
	tokens *auth.TokenService
	mail   mailer.Mailer
	log    *slog.Logger

	resetURL string
	erasers  []UserDataEraser

	loginByEmail *ratelimit.Limiter
	loginByIP    *ratelimit.Limiter
	resetByEmail *ratelimit.Limiter
	resetByIP    *ratelimit.Limiter

	// now is injectable so tests can move time without sleeping.
	now func() time.Time
}

func NewService(repo *Repository, tokens *auth.TokenService, mail mailer.Mailer,
	log *slog.Logger, resetURL string, erasers ...UserDataEraser) *Service {
	return &Service{
		repo:         repo,
		tokens:       tokens,
		mail:         mail,
		log:          log,
		resetURL:     resetURL,
		erasers:      erasers,
		loginByEmail: ratelimit.New(loginAttemptsPerEmail, loginWindow),
		loginByIP:    ratelimit.New(loginAttemptsPerIP, loginWindow),
		resetByEmail: ratelimit.New(resetRequestsPerEmail, resetWindow),
		resetByIP:    ratelimit.New(resetRequestsPerIP, resetWindow),
		now:          time.Now,
	}
}

// Register creates an account and signs the user in.
//
// A duplicate e-mail is reported as a conflict, which does reveal that the address has an
// account. That is inherent to self-service registration — the alternative is to answer
// "check your e-mail" and send the existing owner a notice instead, which is a flow worth
// building later, not a check to skip now.
func (s *Service) Register(ctx context.Context, req registerRequest, userAgent string) (Session, error) {
	if err := req.validate(); err != nil {
		return Session{}, err
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return Session{}, apperr.Internal(err)
	}

	user, err := s.repo.CreateUser(ctx, uuid.New(),
		validate.NormalizeEmail(req.Email), passwordHash, strings.TrimSpace(req.Name))
	switch {
	case errors.Is(err, ErrEmailTaken):
		return Session{}, apperr.Conflict("Este e-mail já está cadastrado.")
	case err != nil:
		return Session{}, apperr.Internal(err)
	}

	return s.issueSession(ctx, user, userAgent)
}

// Login authenticates by e-mail and password.
func (s *Service) Login(ctx context.Context, req loginRequest, userAgent, clientIP string) (Session, error) {
	if err := req.validate(); err != nil {
		return Session{}, err
	}
	email := validate.NormalizeEmail(req.Email)

	// Both limiters are consulted, not short-circuited, so an attacker cycling through
	// e-mail addresses still accrues attempts against their IP.
	emailAllowed := s.loginByEmail.Allow(email)
	ipAllowed := s.loginByIP.Allow(clientIP)
	if !emailAllowed || !ipAllowed {
		return Session{}, apperr.New(apperr.CodeRateLimited,
			"Muitas tentativas de login. Aguarde alguns minutos e tente novamente.")
	}

	user, err := s.repo.UserByEmail(ctx, email)
	switch {
	case errors.Is(err, ErrUserNotFound):
		// Spend the same time a real verification would, so response timing does not
		// reveal which addresses have accounts.
		auth.VerifyDummy(req.Password)
		return Session{}, errInvalidCredentials()
	case err != nil:
		return Session{}, apperr.Internal(err)
	}

	matches, err := auth.VerifyPassword(user.PasswordHash, req.Password)
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	if !matches {
		return Session{}, errInvalidCredentials()
	}

	// Someone who mistyped their password four times and then got it right should not
	// stay one attempt away from a lockout.
	s.loginByEmail.Reset(email)

	return s.issueSession(ctx, user, userAgent)
}

// Refresh rotates a refresh token and issues a new access token.
func (s *Service) Refresh(ctx context.Context, refreshToken, userAgent string) (Session, error) {
	stored, err := s.repo.RefreshTokenByHash(ctx,
		auth.HashOpaqueToken(strings.TrimSpace(refreshToken)))
	switch {
	case errors.Is(err, ErrTokenNotFound):
		return Session{}, errInvalidSession()
	case err != nil:
		return Session{}, apperr.Internal(err)
	}

	if stored.RevokedAt != nil {
		// A token revoked on purpose — a logout, a password reset, an earlier reuse sweep —
		// is simply dead. Replaying one says nothing about anybody holding a copy, and the
		// app replays them routinely: a logout that times out on a bad connection gets
		// retried. Ending every session over that would sign the owner out of their other
		// devices for using the product normally.
		if !revokedByRotation(stored.RevokedReason) {
			return Session{}, errInvalidSession()
		}

		// Reuse detection. A token that was already *rotated* out is being presented again,
		// which means it was captured: the legitimate client holds the successor. There is
		// no way to tell attacker from victim, so every session for the user is ended and
		// both are made to sign in again.
		s.log.Warn("refresh token reuse detected, revoking all sessions",
			slog.String("user_id", stored.UserID.String()))

		if err := s.repo.RevokeAllUserRefreshTokens(ctx, stored.UserID, revokeReasonReuse); err != nil {
			return Session{}, apperr.Internal(err)
		}
		return Session{}, errInvalidSession()
	}

	if !s.now().Before(stored.ExpiresAt) {
		return Session{}, errInvalidSession()
	}

	user, err := s.repo.UserByID(ctx, stored.UserID)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return Session{}, errInvalidSession()
	case err != nil:
		return Session{}, apperr.Internal(err)
	}

	plain, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	refreshExpiresAt := s.now().Add(auth.RefreshTokenTTL)

	if _, err := s.repo.RotateRefreshToken(ctx, stored.ID, stored.UserID,
		hash, refreshExpiresAt, optional(userAgent)); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			// Another request rotated this token first.
			return Session{}, errInvalidSession()
		}
		return Session{}, apperr.Internal(err)
	}

	accessToken, accessExpiresAt, err := s.tokens.IssueAccessToken(user.ID, s.now())
	if err != nil {
		return Session{}, apperr.Internal(err)
	}

	return Session{
		User:             user,
		AccessToken:      accessToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshToken:     plain,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// Logout revokes the presented refresh token.
//
// An unknown token still succeeds: logout is idempotent, and answering differently would
// turn it into an oracle for guessing valid tokens.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	stored, err := s.repo.RefreshTokenByHash(ctx,
		auth.HashOpaqueToken(strings.TrimSpace(refreshToken)))
	switch {
	case errors.Is(err, ErrTokenNotFound):
		return nil
	case err != nil:
		return apperr.Internal(err)
	}

	if err := s.repo.RevokeRefreshTokenOnLogout(ctx, stored.ID); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

// revokedByRotation reports whether a revoked token was rotated out — the only revocation
// whose replay means somebody has a copy.
//
// A missing reason is read as a rotation. The CHECK constraint in migration 000008 makes
// that impossible, and if it ever happens anyway the safe reading is the suspicious one.
func revokedByRotation(reason *string) bool {
	return reason == nil || *reason == revokeReasonRotation
}

// RequestPasswordReset sends a reset link if the address has an account.
//
// It reports success either way. Whether an e-mail address has an account here is not
// something an unauthenticated caller gets to learn — including by timing, which is why
// nothing expensive branches on the lookup.
func (s *Service) RequestPasswordReset(ctx context.Context, req passwordResetRequestRequest, clientIP string) error {
	if err := req.validate(); err != nil {
		return err
	}
	email := validate.NormalizeEmail(req.Email)

	emailAllowed := s.resetByEmail.Allow(email)
	ipAllowed := s.resetByIP.Allow(clientIP)
	if !emailAllowed || !ipAllowed {
		return apperr.New(apperr.CodeRateLimited,
			"Muitas solicitações. Aguarde alguns minutos e tente novamente.")
	}

	user, err := s.repo.UserByEmail(ctx, email)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return nil
	case err != nil:
		return apperr.Internal(err)
	}

	plain, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return apperr.Internal(err)
	}

	if err := s.repo.CreatePasswordResetToken(ctx, user.ID, hash,
		s.now().Add(auth.PasswordResetTTL)); err != nil {
		return apperr.Internal(err)
	}

	resetURL := s.resetURL + "?token=" + url.QueryEscape(plain)
	if err := s.mail.SendPasswordReset(ctx, user.Email, user.Name, resetURL); err != nil {
		// Logged, not returned. Surfacing a provider failure here would make an existing
		// address answer differently from an unknown one, undoing the whole point of
		// always reporting success.
		s.log.Error("failed to send password reset e-mail",
			slog.String("user_id", user.ID.String()),
			slog.Any("error", err))
	}
	return nil
}

// ConfirmPasswordReset sets a new password from a reset token.
func (s *Service) ConfirmPasswordReset(ctx context.Context, req passwordResetConfirmRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	token, err := s.repo.PasswordResetTokenByHash(ctx,
		auth.HashOpaqueToken(strings.TrimSpace(req.Token)))
	switch {
	case errors.Is(err, ErrTokenNotFound):
		return errInvalidResetToken()
	case err != nil:
		return apperr.Internal(err)
	}

	if token.UsedAt != nil || !s.now().Before(token.ExpiresAt) {
		return errInvalidResetToken()
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return apperr.Internal(err)
	}

	if err := s.repo.CompletePasswordReset(ctx, token.ID, token.UserID, passwordHash); err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return errInvalidResetToken()
		}
		return apperr.Internal(err)
	}
	return nil
}

// Me returns the authenticated user.
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (db.User, error) {
	user, err := s.repo.UserByID(ctx, userID)
	switch {
	case errors.Is(err, ErrUserNotFound):
		// The token is valid but the account is gone — deleted while the token was still
		// within its 15 minutes.
		return db.User{}, apperr.Unauthorized("Sessão inválida. Entre novamente.")
	case err != nil:
		return db.User{}, apperr.Internal(err)
	}
	return user, nil
}

// UpdateName changes the display name.
func (s *Service) UpdateName(ctx context.Context, userID uuid.UUID, req updateMeRequest) (db.User, error) {
	if err := req.validate(); err != nil {
		return db.User{}, err
	}

	user, err := s.repo.UpdateUserName(ctx, userID, strings.TrimSpace(req.Name))
	switch {
	case errors.Is(err, ErrUserNotFound):
		return db.User{}, apperr.Unauthorized("Sessão inválida. Entre novamente.")
	case err != nil:
		return db.User{}, apperr.Internal(err)
	}
	return user, nil
}

// DeleteAccount erases the account and everything that cascades from it.
//
// The current password is required. This is irreversible and takes every vehicle and
// record with it, so a stolen access token must not be enough to trigger it.
func (s *Service) DeleteAccount(ctx context.Context, userID uuid.UUID, req deleteMeRequest) error {
	if err := req.validate(); err != nil {
		return err
	}

	user, err := s.repo.UserByID(ctx, userID)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return apperr.Unauthorized("Sessão inválida. Entre novamente.")
	case err != nil:
		return apperr.Internal(err)
	}

	matches, err := auth.VerifyPassword(user.PasswordHash, req.Password)
	if err != nil {
		return apperr.Internal(err)
	}
	if !matches {
		return apperr.Unauthorized("Senha incorreta.")
	}

	// Erasers run first, and the users row is deleted last.
	//
	// The two are not in one transaction — each eraser owns its own. The ordering is what
	// makes a partial failure recoverable: if an eraser fails, nothing has been lost and
	// the caller can retry. If the final delete failed instead, the account would survive
	// with its data already gone, and the retry still completes the job.
	for _, eraser := range s.erasers {
		if err := eraser.EraseUserData(ctx, userID); err != nil {
			s.log.Error("failed to erase user data, account not deleted",
				slog.String("user_id", userID.String()),
				slog.Any("error", err))
			return apperr.Internal(err)
		}
	}

	if err := s.repo.DeleteUser(ctx, userID); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil
		}
		return apperr.Internal(err)
	}
	return nil
}

func (s *Service) issueSession(ctx context.Context, user db.User, userAgent string) (Session, error) {
	now := s.now()

	accessToken, accessExpiresAt, err := s.tokens.IssueAccessToken(user.ID, now)
	if err != nil {
		return Session{}, apperr.Internal(err)
	}

	plain, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	refreshExpiresAt := now.Add(auth.RefreshTokenTTL)

	if _, err := s.repo.CreateRefreshToken(ctx, user.ID, hash,
		refreshExpiresAt, optional(userAgent)); err != nil {
		return Session{}, apperr.Internal(err)
	}

	return Session{
		User:             user,
		AccessToken:      accessToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshToken:     plain,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// Every failed authentication answers identically. Distinguishing "no such account" from
// "wrong password" hands an attacker a list of valid e-mail addresses.
func errInvalidCredentials() error {
	return apperr.Unauthorized("E-mail ou senha incorretos.")
}

func errInvalidSession() error {
	return apperr.Unauthorized("Sessão inválida ou expirada. Entre novamente.")
}

func errInvalidResetToken() error {
	return apperr.Unauthorized("Link de redefinição inválido ou expirado.")
}

// optional turns an empty string into a NULL column rather than an empty one.
func optional(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}
