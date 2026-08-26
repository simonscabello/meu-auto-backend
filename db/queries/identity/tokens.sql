-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, user_agent)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash = $1;

-- Three revocations rather than one, because revoked_reason is what lets Refresh tell a
-- replayed rotated token (somebody has a copy) from a replayed logout (a retry). See
-- migration 000008.

-- Rotation: revoke the presented token and point it at its successor, so a replayed old
-- token can be traced to the chain it belonged to.
-- name: RevokeRefreshTokenOnRotation :execrows
UPDATE refresh_tokens
SET revoked_at = now(), revoked_reason = 'rotation', replaced_by = $2
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokenOnLogout :execrows
UPDATE refresh_tokens
SET revoked_at = now(), revoked_reason = 'logout'
WHERE id = $1 AND revoked_at IS NULL;

-- Used on reuse detection and after a password reset — the reason says which.
-- name: RevokeAllUserRefreshTokens :execrows
UPDATE refresh_tokens
SET revoked_at = now(), revoked_reason = sqlc.arg('reason')::text
WHERE user_id = sqlc.arg('user_id') AND revoked_at IS NULL;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at < now();

-- name: CreatePasswordResetToken :one
INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetPasswordResetTokenByHash :one
SELECT * FROM password_reset_tokens WHERE token_hash = $1;

-- name: MarkPasswordResetTokenUsed :execrows
UPDATE password_reset_tokens
SET used_at = now()
WHERE id = $1 AND used_at IS NULL;

-- Requesting a new reset link invalidates any outstanding one, so an e-mail that leaks
-- later cannot still be redeemed.
-- name: InvalidateUserPasswordResetTokens :execrows
UPDATE password_reset_tokens
SET used_at = now()
WHERE user_id = $1 AND used_at IS NULL;
