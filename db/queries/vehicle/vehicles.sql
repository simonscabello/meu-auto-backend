-- The id comes from the client (UUIDv7). ON CONFLICT DO NOTHING makes a retried create
-- idempotent: a mobile client that loses signal mid-request can safely send it again.
-- name: CreateVehicle :one
INSERT INTO vehicles (
    id, vehicle_type, brand, model, version,
    manufacture_year, model_year, plate, renavam, chassis,
    fuel_type, color, nickname, fipe_code,
    -- The link to the catalogue entry the owner picked. Always nullable: a vehicle typed
    -- in by hand is still a vehicle, and the app already installed never sends these.
    catalog_brand_id, catalog_model_id, catalog_model_year_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
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

-- PATCH semantics: a NULL argument leaves the column untouched. Optional columns named
-- in `clear` are set back to NULL. brand and model are NOT NULL and are not accepted
-- there — emptying them is not an edit, it is destroying the identity of the car.
-- name: UpdateVehicle :one
UPDATE vehicles
SET brand            = COALESCE(sqlc.narg('brand'), brand),
    model            = COALESCE(sqlc.narg('model'), model),
    version          = CASE WHEN 'version' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('version'), version) END,
    manufacture_year = CASE WHEN 'manufacture_year' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('manufacture_year'), manufacture_year) END,
    model_year       = CASE WHEN 'model_year' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('model_year'), model_year) END,
    plate            = CASE WHEN 'plate' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('plate'), plate) END,
    renavam          = CASE WHEN 'renavam' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('renavam'), renavam) END,
    chassis          = CASE WHEN 'chassis' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('chassis'), chassis) END,
    fuel_type        = CASE WHEN 'fuel_type' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('fuel_type'), fuel_type) END,
    color            = CASE WHEN 'color' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('color'), color) END,
    nickname         = CASE WHEN 'nickname' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('nickname'), nickname) END,
    fipe_code        = CASE WHEN 'fipe_code' = ANY(sqlc.arg('clear')::text[]) THEN NULL
                            ELSE COALESCE(sqlc.narg('fipe_code'), fipe_code) END,

    -- The three move together or not at all. They are resolved from a single
    -- catalog_model_year_id, so a partial update could not produce a brand that disagrees
    -- with its model — but writing them as one group is what keeps that true if somebody
    -- later adds a second way in.
    catalog_brand_id      = COALESCE(sqlc.narg('catalog_brand_id'), catalog_brand_id),
    catalog_model_id      = COALESCE(sqlc.narg('catalog_model_id'), catalog_model_id),
    catalog_model_year_id = COALESCE(sqlc.narg('catalog_model_year_id'), catalog_model_year_id),

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
