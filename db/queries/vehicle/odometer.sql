-- name: CreateOdometerReading :one
INSERT INTO odometer_readings (
    id, vehicle_id, mileage_km, occurred_on, source, recorded_by_user_id, notes
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: GetOdometerReading :one
SELECT * FROM odometer_readings WHERE id = $1;

-- name: DeleteOdometerReading :execrows
DELETE FROM odometer_readings WHERE id = $1;

-- Keyset pagination, never OFFSET: readings are appended constantly, and an offset page
-- silently skips or repeats rows whenever something is inserted between two requests.
-- name: ListOdometerReadings :many
SELECT *
FROM odometer_readings
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

-- The monotonicity check for a new reading (SPEC.md RN-01).
--
-- It compares against the reading's NEIGHBOURS IN TIME, not against the vehicle's current
-- mileage. That difference matters: someone entering a reading they forgot three months
-- ago is legitimately recording a lower number, and comparing against "current" would
-- reject it. What is actually invalid is a reading that contradicts the entries around it.
--
-- Two queries rather than one with scalar subqueries: sqlc infers a scalar subquery as
-- NOT NULL, which is wrong here — a vehicle's first reading has no neighbour on either
-- side, and scanning that NULL into an int32 fails at runtime. Splitting them makes
-- "no neighbour" an unambiguous pgx.ErrNoRows.

--
-- exclude_source_id names the event being EDITED. A record or a fill that moves its own
-- mileage must not be compared against the reading it produced itself: correcting a typo
-- from 105.000 to 104.000 km on the latest record used to be refused as a rollback against
-- its own old value. NULL for a new reading, which has nothing of its own to skip.

-- name: GetPreviousOdometerReading :one
SELECT *
FROM odometer_readings
WHERE vehicle_id = sqlc.arg(vehicle_id)
  AND occurred_on <= sqlc.arg(occurred_on)
  AND (sqlc.narg(exclude_source_id)::uuid IS NULL
       OR (source_maintenance_id IS DISTINCT FROM sqlc.narg(exclude_source_id)::uuid
           AND source_abastecimento_id IS DISTINCT FROM sqlc.narg(exclude_source_id)::uuid))
ORDER BY occurred_on DESC, created_at DESC
LIMIT 1;

-- name: GetNextOdometerReading :one
SELECT *
FROM odometer_readings
WHERE vehicle_id = sqlc.arg(vehicle_id)
  AND occurred_on > sqlc.arg(occurred_on)
  AND (sqlc.narg(exclude_source_id)::uuid IS NULL
       OR (source_maintenance_id IS DISTINCT FROM sqlc.narg(exclude_source_id)::uuid
           AND source_abastecimento_id IS DISTINCT FROM sqlc.narg(exclude_source_id)::uuid))
ORDER BY occurred_on ASC, created_at ASC
LIMIT 1;
