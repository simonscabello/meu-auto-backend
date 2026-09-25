-- name: CreateUser :one
INSERT INTO users (id, email, password_hash, name)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- PATCH semantics: a NULL argument leaves the column untouched, and a column named in
-- `clear` is set back to NULL. name is NOT NULL and is never in `clear`.
-- name: UpdateUserProfile :one
UPDATE users
SET name           = COALESCE(sqlc.narg('name'), name),
    birth_date     = CASE WHEN 'birth_date' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                          ELSE COALESCE(sqlc.narg('birth_date'), birth_date) END,
    phone          = CASE WHEN 'phone' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                          ELSE COALESCE(sqlc.narg('phone'), phone) END,
    cnh_category   = CASE WHEN 'cnh_category' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                          ELSE COALESCE(sqlc.narg('cnh_category'), cnh_category) END,
    cnh_expires_on = CASE WHEN 'cnh_expires_on' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                          ELSE COALESCE(sqlc.narg('cnh_expires_on'), cnh_expires_on) END,
    updated_at     = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- Reads the photo a user has now, locking the row so a concurrent upload waits and then
-- sees this one's key. Called inside the transaction that sets the new one.
-- name: LockUserPhotoKey :one
SELECT photo_key FROM users WHERE id = $1 FOR UPDATE;

-- name: SetUserPhotoKey :one
UPDATE users
SET photo_key  = sqlc.narg('photo_key'),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: UpdateUserPassword :execrows
UPDATE users
SET password_hash = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateUserPasswordIfCurrent :execrows
UPDATE users
SET password_hash = sqlc.arg('new_password_hash'), updated_at = now()
WHERE id = sqlc.arg('id')
  AND password_hash = sqlc.arg('current_password_hash');

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;
