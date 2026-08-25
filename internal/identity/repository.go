package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/meu-auto/meu-auto-backend/internal/identity/db"
)

// Domain-level failures the repository reports.
//
// They are plain sentinels, not apperr values: the repository does not decide what the
// client is told. The service owns that mapping, which keeps every client-visible message
// for this module in one file.
var (
	ErrUserNotFound  = errors.New("identity: user not found")
	ErrEmailTaken    = errors.New("identity: e-mail already registered")
	ErrTokenNotFound = errors.New("identity: token not found")
)

const pgUniqueViolation = "23505"

// Repository is the module's data access. Multi-step writes are exposed as single methods
// that own their transaction, so no caller can perform half of one.
type Repository struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: db.New(pool)}
}

func (r *Repository) inTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// A no-op once Commit has succeeded, and the safety net on every early return.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(r.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ---------- users ----------

func (r *Repository) CreateUser(ctx context.Context, id uuid.UUID, email, passwordHash, name string) (db.User, error) {
	user, err := r.queries.CreateUser(ctx, db.CreateUserParams{
		ID:           id,
		Email:        email,
		PasswordHash: passwordHash,
		Name:         name,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation &&
			pgErr.ConstraintName == "users_email_key" {
			return db.User{}, ErrEmailTaken
		}
		return db.User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

func (r *Repository) UserByID(ctx context.Context, id uuid.UUID) (db.User, error) {
	user, err := r.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, ErrUserNotFound
		}
		return db.User{}, fmt.Errorf("get user by id: %w", err)
	}
	return user, nil
}

func (r *Repository) UserByEmail(ctx context.Context, email string) (db.User, error) {
	user, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, ErrUserNotFound
		}
		return db.User{}, fmt.Errorf("get user by e-mail: %w", err)
	}
	return user, nil
}

func (r *Repository) UpdateUserName(ctx context.Context, id uuid.UUID, name string) (db.User, error) {
	user, err := r.queries.UpdateUserName(ctx, db.UpdateUserNameParams{ID: id, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, ErrUserNotFound
		}
		return db.User{}, fmt.Errorf("update user name: %w", err)
	}
	return user, nil
}

// DeleteUser removes the account. Every dependent row is removed with it by ON DELETE
// CASCADE — that is the LGPD erasure path, not a soft delete (SPEC.md D-10).
func (r *Repository) DeleteUser(ctx context.Context, id uuid.UUID) error {
	rows, err := r.queries.DeleteUser(ctx, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if rows == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ---------- refresh tokens ----------

func (r *Repository) CreateRefreshToken(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time, userAgent *string) (db.RefreshToken, error) {
	token, err := r.queries.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: hash,
		ExpiresAt: expiresAt,
		UserAgent: userAgent,
	})
	if err != nil {
		return db.RefreshToken{}, fmt.Errorf("create refresh token: %w", err)
	}
	return token, nil
}

func (r *Repository) RefreshTokenByHash(ctx context.Context, hash []byte) (db.RefreshToken, error) {
	token, err := r.queries.GetRefreshTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RefreshToken{}, ErrTokenNotFound
		}
		return db.RefreshToken{}, fmt.Errorf("get refresh token: %w", err)
	}
	return token, nil
}

// RevokeRefreshToken revokes a single token. Used by logout.
func (r *Repository) RevokeRefreshToken(ctx context.Context, id uuid.UUID) error {
	if _, err := r.queries.RevokeRefreshToken(ctx, db.RevokeRefreshTokenParams{ID: id}); err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

// RevokeAllUserRefreshTokens ends every session for a user. Used on reuse detection and
// after a password reset.
func (r *Repository) RevokeAllUserRefreshTokens(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.queries.RevokeAllUserRefreshTokens(ctx, userID); err != nil {
		return fmt.Errorf("revoke user refresh tokens: %w", err)
	}
	return nil
}

// RotateRefreshToken issues a successor and revokes the presented token, atomically.
//
// Atomicity is the point. If the two steps could be split, a crash between them either
// leaves the old token valid alongside the new one, or leaves the user with no valid token
// at all.
func (r *Repository) RotateRefreshToken(ctx context.Context, oldID, userID uuid.UUID, newHash []byte, expiresAt time.Time, userAgent *string) (db.RefreshToken, error) {
	var created db.RefreshToken

	err := r.inTx(ctx, func(q *db.Queries) error {
		var err error
		created, err = q.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
			ID:        uuid.New(),
			UserID:    userID,
			TokenHash: newHash,
			ExpiresAt: expiresAt,
			UserAgent: userAgent,
		})
		if err != nil {
			return fmt.Errorf("create successor refresh token: %w", err)
		}

		rows, err := q.RevokeRefreshToken(ctx, db.RevokeRefreshTokenParams{
			ID:         oldID,
			ReplacedBy: &created.ID,
		})
		if err != nil {
			return fmt.Errorf("revoke rotated refresh token: %w", err)
		}
		// The query only matches tokens that are still active. Zero rows means another
		// request rotated this same token first — a race, or a replay. Rolling back is
		// what keeps exactly one successor per token.
		if rows == 0 {
			return ErrTokenNotFound
		}
		return nil
	})
	if err != nil {
		return db.RefreshToken{}, err
	}
	return created, nil
}

// ---------- password reset ----------

// CreatePasswordResetToken invalidates any outstanding token for the user and issues one,
// atomically, so a reset e-mail that leaks later cannot still be redeemed.
func (r *Repository) CreatePasswordResetToken(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error {
	return r.inTx(ctx, func(q *db.Queries) error {
		if _, err := q.InvalidateUserPasswordResetTokens(ctx, userID); err != nil {
			return fmt.Errorf("invalidate previous reset tokens: %w", err)
		}
		if _, err := q.CreatePasswordResetToken(ctx, db.CreatePasswordResetTokenParams{
			ID:        uuid.New(),
			UserID:    userID,
			TokenHash: hash,
			ExpiresAt: expiresAt,
		}); err != nil {
			return fmt.Errorf("create reset token: %w", err)
		}
		return nil
	})
}

func (r *Repository) PasswordResetTokenByHash(ctx context.Context, hash []byte) (db.PasswordResetToken, error) {
	token, err := r.queries.GetPasswordResetTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PasswordResetToken{}, ErrTokenNotFound
		}
		return db.PasswordResetToken{}, fmt.Errorf("get reset token: %w", err)
	}
	return token, nil
}

// CompletePasswordReset sets the new password, consumes the token and ends every existing
// session, atomically.
//
// Revoking the sessions is the part that matters: someone resetting their password because
// an attacker got in has achieved nothing if the attacker's refresh token still works.
func (r *Repository) CompletePasswordReset(ctx context.Context, tokenID, userID uuid.UUID, passwordHash string) error {
	return r.inTx(ctx, func(q *db.Queries) error {
		// Consume the token first: zero rows means it was already redeemed by a
		// concurrent request, and the rollback keeps that from happening twice.
		rows, err := q.MarkPasswordResetTokenUsed(ctx, tokenID)
		if err != nil {
			return fmt.Errorf("consume reset token: %w", err)
		}
		if rows == 0 {
			return ErrTokenNotFound
		}

		if _, err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
			ID:           userID,
			PasswordHash: passwordHash,
		}); err != nil {
			return fmt.Errorf("update password: %w", err)
		}

		if _, err := q.RevokeAllUserRefreshTokens(ctx, userID); err != nil {
			return fmt.Errorf("revoke sessions after reset: %w", err)
		}
		return nil
	})
}
