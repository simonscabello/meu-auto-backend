-- name: CreateMaintenanceRecord :one
INSERT INTO maintenance_records (
    id, vehicle_id, occurred_on, mileage_km, kind,
    workshop_name, total_cost_cents, notes, recorded_by_user_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: CreateMaintenanceRecordItem :one
INSERT INTO maintenance_record_items (
    maintenance_record_id, maintenance_item_id,
    description, part_brand, cost_cents, warranty_months, warranty_km
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- Used only on the idempotency path, immediately after an id conflict, and always with
-- the caller's vehicle already authorised and the returned vehicle_id compared. It is not
-- an authorisation bypass; every request-facing read uses the query below.
-- name: GetMaintenanceRecordByID :one
SELECT * FROM maintenance_records WHERE id = $1;

-- Records are read through the vehicle, never by id alone — the same rule as vehicles
-- (SPEC.md RN-07). The join is the authorisation.
-- name: GetMaintenanceRecordForUser :one
SELECT r.*
FROM maintenance_records r
JOIN vehicle_ownerships o ON o.vehicle_id = r.vehicle_id
WHERE r.id = $1
  AND o.user_id = $2
  AND o.ended_on IS NULL
  AND r.deleted_at IS NULL;

-- Keyset pagination, newest first.
-- name: ListMaintenanceRecordsForVehicle :many
SELECT *
FROM maintenance_records
WHERE vehicle_id = sqlc.arg('vehicle_id')
  AND deleted_at IS NULL
  AND (
        sqlc.narg('cursor_occurred_on')::date IS NULL
        OR (occurred_on, created_at, id) <
           (sqlc.narg('cursor_occurred_on')::date,
            sqlc.narg('cursor_created_at')::timestamptz,
            sqlc.narg('cursor_id')::uuid)
      )
ORDER BY occurred_on DESC, created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- One query for a whole page of records, rather than one per record: the N+1 here would
-- be a page-sized burst of round trips for no reason.
-- name: ListMaintenanceRecordItems :many
SELECT ri.*, i.slug AS item_slug, i.name AS item_name, i.kind AS item_kind
FROM maintenance_record_items ri
JOIN maintenance_items i ON i.id = ri.maintenance_item_id
WHERE ri.maintenance_record_id = ANY(sqlc.arg('record_ids')::uuid[])
ORDER BY i.name;

-- name: UpdateMaintenanceRecord :one
UPDATE maintenance_records
SET occurred_on      = COALESCE(sqlc.narg('occurred_on'), occurred_on),
    mileage_km       = COALESCE(sqlc.narg('mileage_km'), mileage_km),
    workshop_name    = COALESCE(sqlc.narg('workshop_name'), workshop_name),
    total_cost_cents = COALESCE(sqlc.narg('total_cost_cents'), total_cost_cents),
    notes            = COALESCE(sqlc.narg('notes'), notes),
    updated_at       = now()
WHERE id = sqlc.arg('id') AND deleted_at IS NULL
RETURNING *;

-- Soft delete: a service record is the product's asset at resale, and one mistaken tap
-- must not destroy it.
-- name: SoftDeleteMaintenanceRecord :execrows
UPDATE maintenance_records
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- The baseline for every plan on a vehicle: the most recent record naming each item.
--
-- DISTINCT ON collapses to one row per item, and the ORDER BY inside it is exactly the
-- rule from SPEC.md RN-02 — latest date wins, latest entry breaks the tie. Soft-deleted
-- records are excluded, so retracting a service correctly moves the clock back.
--
-- Keyed by item rather than by plan on purpose: a line inside a multi-item revisão must
-- reset that item's clock just as a standalone record would.
-- name: ListLastPerformedByItem :many
SELECT DISTINCT ON (ri.maintenance_item_id)
       ri.maintenance_item_id,
       r.id          AS record_id,
       r.occurred_on AS occurred_on,
       r.mileage_km  AS mileage_km
FROM maintenance_record_items ri
JOIN maintenance_records r ON r.id = ri.maintenance_record_id
WHERE r.vehicle_id = $1
  AND r.deleted_at IS NULL
ORDER BY ri.maintenance_item_id, r.occurred_on DESC, r.created_at DESC;

-- Writes to odometer_readings from this module.
--
-- odometer_readings is co-owned by design: the vehicle module owns manual readings and
-- every read of the log, but any module may append a reading tagged with its own source,
-- inside its own transaction. The alternative — calling the vehicle service — would put
-- the reading outside the record's transaction and break the atomicity SPEC.md RN-01
-- requires. The cache stays correct either way, because the trigger does not care who
-- wrote the row.
-- name: CreateMaintenanceOdometerReading :exec
INSERT INTO odometer_readings (
    vehicle_id, mileage_km, occurred_on, source, recorded_by_user_id, source_maintenance_id
)
VALUES ($1, $2, $3, 'maintenance', $4, $5);

-- name: DeleteMaintenanceOdometerReading :exec
DELETE FROM odometer_readings WHERE source_maintenance_id = $1;

-- name: UpdateMaintenanceOdometerReading :exec
UPDATE odometer_readings
SET mileage_km = $2, occurred_on = $3
WHERE source_maintenance_id = $1;

-- Every line item that carries a warranty, with the record's date and mileage so the
-- expiry can be derived. Warranty end is never stored (SPEC.md RN-05).
-- name: ListWarrantiesForVehicle :many
SELECT ri.id,
       ri.maintenance_item_id,
       ri.warranty_months,
       ri.warranty_km,
       i.name        AS item_name,
       r.id          AS record_id,
       r.occurred_on AS record_occurred_on,
       r.mileage_km  AS record_mileage_km
FROM maintenance_record_items ri
JOIN maintenance_records r ON r.id = ri.maintenance_record_id
JOIN maintenance_items i ON i.id = ri.maintenance_item_id
WHERE r.vehicle_id = $1
  AND r.deleted_at IS NULL
  AND (ri.warranty_months IS NOT NULL OR ri.warranty_km IS NOT NULL)
ORDER BY r.occurred_on DESC;
