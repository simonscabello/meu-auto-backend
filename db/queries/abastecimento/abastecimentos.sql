-- name: CreateAbastecimento :one
INSERT INTO abastecimentos (
    id, vehicle_id, occurred_on, mileage_km, volume_ml, total_cost_cents,
    fuel, full_tank, station_name, notes, recorded_by_user_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- Used only on the idempotency path, immediately after an id conflict, and always with
-- the caller's vehicle already authorised and the returned vehicle_id compared. It is not
-- an authorisation bypass; every request-facing read uses the query below.
-- name: GetAbastecimentoByID :one
SELECT * FROM abastecimentos WHERE id = $1;

-- Fills are read through the vehicle, never by id alone — the same rule as vehicles
-- (SPEC.md RN-07). The join is the authorisation.
-- name: GetAbastecimentoForUser :one
SELECT a.*
FROM abastecimentos a
JOIN vehicle_ownerships o ON o.vehicle_id = a.vehicle_id
WHERE a.id = $1
  AND o.user_id = $2
  AND o.ended_on IS NULL;

-- Keyset pagination, newest first.
-- name: ListAbastecimentosForVehicle :many
SELECT *
FROM abastecimentos
WHERE vehicle_id = sqlc.arg('vehicle_id')
  AND (
        sqlc.narg('cursor_occurred_on')::date IS NULL
        OR (occurred_on, created_at, id) <
           (sqlc.narg('cursor_occurred_on')::date,
            sqlc.narg('cursor_created_at')::timestamptz,
            sqlc.narg('cursor_id')::uuid)
      )
ORDER BY occurred_on DESC, created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- The full history, oldest first, for the consumption walk. Consumption is never stored,
-- so every read recomputes from this sequence.
-- name: ListAllAbastecimentosForVehicle :many
SELECT *
FROM abastecimentos
WHERE vehicle_id = $1
ORDER BY occurred_on ASC, created_at ASC, id ASC;

-- name: UpdateAbastecimento :one
UPDATE abastecimentos
SET occurred_on      = COALESCE(sqlc.narg('occurred_on'), occurred_on),
    mileage_km       = COALESCE(sqlc.narg('mileage_km'), mileage_km),
    volume_ml        = COALESCE(sqlc.narg('volume_ml'), volume_ml),
    total_cost_cents = COALESCE(sqlc.narg('total_cost_cents'), total_cost_cents),
    fuel             = COALESCE(sqlc.narg('fuel'), fuel),
    full_tank        = COALESCE(sqlc.narg('full_tank'), full_tank),
    station_name     = COALESCE(sqlc.narg('station_name'), station_name),
    notes            = COALESCE(sqlc.narg('notes'), notes),
    updated_at       = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteAbastecimento :execrows
DELETE FROM abastecimentos WHERE id = $1;

-- Writes to odometer_readings from this module.
--
-- odometer_readings is co-owned by design: the vehicle module owns manual readings and
-- every read of the log, but any module may append a reading tagged with its own source,
-- inside its own transaction. The cache stays correct either way, because the trigger
-- does not care who wrote the row.
-- name: CreateAbastecimentoOdometerReading :exec
INSERT INTO odometer_readings (
    vehicle_id, mileage_km, occurred_on, source, recorded_by_user_id, source_abastecimento_id
)
VALUES ($1, $2, $3, 'abastecimento', $4, $5);

-- name: UpdateAbastecimentoOdometerReading :exec
UPDATE odometer_readings
SET mileage_km = $2, occurred_on = $3
WHERE source_abastecimento_id = $1;
