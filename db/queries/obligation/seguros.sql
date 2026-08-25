-- name: CreateSeguro :one
INSERT INTO seguros (
    id, vehicle_id, insurer_name, policy_number, starts_on, ends_on,
    premium_cents, emergency_phone, broker_name, broker_phone, notes,
    recorded_by_user_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- Newest period first: the current policy is what the owner opens the screen for.
-- name: ListSegurosForVehicle :many
SELECT * FROM seguros
WHERE vehicle_id = $1
ORDER BY ends_on DESC;

-- name: GetSeguroForUser :one
SELECT s.*
FROM seguros s
JOIN vehicle_ownerships vo ON vo.vehicle_id = s.vehicle_id
WHERE s.id = $1
  AND vo.user_id = $2
  AND vo.ended_on IS NULL;

-- name: GetSeguroByID :one
SELECT * FROM seguros WHERE id = $1;

-- name: UpdateSeguro :one
UPDATE seguros
SET insurer_name    = COALESCE(sqlc.narg('insurer_name'), insurer_name),
    policy_number   = COALESCE(sqlc.narg('policy_number'), policy_number),
    starts_on       = COALESCE(sqlc.narg('starts_on'), starts_on),
    ends_on         = COALESCE(sqlc.narg('ends_on'), ends_on),
    premium_cents   = COALESCE(sqlc.narg('premium_cents'), premium_cents),
    emergency_phone = COALESCE(sqlc.narg('emergency_phone'), emergency_phone),
    broker_name     = COALESCE(sqlc.narg('broker_name'), broker_name),
    broker_phone    = COALESCE(sqlc.narg('broker_phone'), broker_phone),
    notes           = COALESCE(sqlc.narg('notes'), notes),
    updated_at      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- Hard delete: a policy is the owner's private record — insurer, broker and phone numbers
-- are exactly what must NOT travel with the vehicle on resale (SPEC.md RN-10).
-- name: DeleteSeguro :execrows
DELETE FROM seguros WHERE id = $1;
