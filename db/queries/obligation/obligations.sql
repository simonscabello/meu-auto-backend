-- name: CreateObligation :one
INSERT INTO vehicle_obligations (
    id, vehicle_id, kind, reference_year, due_on, amount_cents,
    paid_on, paid_amount_cents, notes, recorded_by_user_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (vehicle_id, kind, reference_year) DO NOTHING
RETURNING *;

-- name: ListObligationsForVehicle :many
SELECT *
FROM vehicle_obligations
WHERE vehicle_id = sqlc.arg('vehicle_id')
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
ORDER BY due_on DESC;

-- Read through ownership, never by id alone (SPEC.md RN-07). The join is the
-- authorisation.
-- name: GetObligationForUser :one
SELECT o.*
FROM vehicle_obligations o
JOIN vehicle_ownerships vo ON vo.vehicle_id = o.vehicle_id
WHERE o.id = $1
  AND vo.user_id = $2
  AND vo.ended_on IS NULL;

-- PATCH semantics: a NULL argument leaves the column alone.
--
-- Marking an obligation unpaid needs its own flag, because NULL already means "unchanged"
-- and undoing a mistaken "paid" is a thing people do.
-- name: UpdateObligation :one
UPDATE vehicle_obligations
SET due_on            = COALESCE(sqlc.narg('due_on'), due_on),
    amount_cents      = COALESCE(sqlc.narg('amount_cents'), amount_cents),
    paid_on           = CASE WHEN sqlc.arg('clear_payment')::boolean THEN NULL
                             ELSE COALESCE(sqlc.narg('paid_on'), paid_on) END,
    paid_amount_cents = CASE WHEN sqlc.arg('clear_payment')::boolean THEN NULL
                             ELSE COALESCE(sqlc.narg('paid_amount_cents'), paid_amount_cents) END,
    notes             = COALESCE(sqlc.narg('notes'), notes),
    updated_at        = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- Hard delete: an obligation is the owner's private record, not vehicle history that
-- travels on resale (SPEC.md RN-10).
-- name: DeleteObligation :execrows
DELETE FROM vehicle_obligations WHERE id = $1;
