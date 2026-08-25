-- The id comes from the client (UUIDv7). ON CONFLICT DO NOTHING makes a retried create
-- idempotent: a mobile client that loses signal mid-request can safely send it again.
-- name: CreateVehicle :one
INSERT INTO vehicles (
    id, vehicle_type, brand, model, version,
    manufacture_year, model_year, plate, renavam, chassis,
    fuel_type, color, nickname
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: CreateOwnership :one
INSERT INTO vehicle_ownerships (vehicle_id, user_id, role, started_on)
VALUES ($1, $2, 'owner', CURRENT_DATE)
RETURNING *;

-- Every read of a vehicle goes through ownership. There is no query in this module that
-- fetches a vehicle by id alone (SPEC.md RN-07).
-- name: GetVehicleForUser :one
SELECT v.*
FROM vehicles v
JOIN vehicle_ownerships o ON o.vehicle_id = v.id
WHERE v.id = $1
  AND o.user_id = $2
  AND o.ended_on IS NULL
  AND v.deleted_at IS NULL;

-- name: ListVehiclesForUser :many
SELECT v.*
FROM vehicles v
JOIN vehicle_ownerships o ON o.vehicle_id = v.id
WHERE o.user_id = $1
  AND o.ended_on IS NULL
  AND v.deleted_at IS NULL
ORDER BY v.created_at ASC;

-- PATCH semantics: a NULL argument leaves the column untouched. The trade-off is that an
-- optional field cannot be cleared back to NULL through this query — doing that needs an
-- explicit "clear" affordance, which nothing has asked for yet.
-- name: UpdateVehicle :one
UPDATE vehicles
SET brand            = COALESCE(sqlc.narg('brand'), brand),
    model            = COALESCE(sqlc.narg('model'), model),
    version          = COALESCE(sqlc.narg('version'), version),
    manufacture_year = COALESCE(sqlc.narg('manufacture_year'), manufacture_year),
    model_year       = COALESCE(sqlc.narg('model_year'), model_year),
    plate            = COALESCE(sqlc.narg('plate'), plate),
    renavam          = COALESCE(sqlc.narg('renavam'), renavam),
    chassis          = COALESCE(sqlc.narg('chassis'), chassis),
    fuel_type        = COALESCE(sqlc.narg('fuel_type'), fuel_type),
    color            = COALESCE(sqlc.narg('color'), color),
    nickname         = COALESCE(sqlc.narg('nickname'), nickname),
    updated_at       = now()
WHERE id = sqlc.arg('id') AND deleted_at IS NULL
RETURNING *;

-- Soft delete: the service history is the product's asset, and one mistaken tap must not
-- destroy years of it.
-- name: SoftDeleteVehicle :execrows
UPDATE vehicles
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- There is no query here that refreshes vehicles.current_mileage_km. The invariant is
-- enforced by a trigger on odometer_readings (migration 000006), because more than one
-- module writes readings and a module that forgets to refresh would silently serve a
-- stale mileage rather than fail.

-- LGPD erasure. Removes vehicles this user owns and nobody else does, including
-- soft-deleted ones; everything else cascades from vehicles.
--
-- The NOT EXISTS clause is inert today (a vehicle has one owner) and is what keeps this
-- correct once a vehicle can be shared: erasing one person must not delete the co-owner's
-- car.
-- name: DeleteVehiclesOwnedSolelyBy :execrows
DELETE FROM vehicles v
WHERE EXISTS (
        SELECT 1 FROM vehicle_ownerships o
        WHERE o.vehicle_id = v.id AND o.user_id = $1 AND o.ended_on IS NULL
      )
  AND NOT EXISTS (
        SELECT 1 FROM vehicle_ownerships o2
        WHERE o2.vehicle_id = v.id AND o2.user_id <> $1 AND o2.ended_on IS NULL
      );
