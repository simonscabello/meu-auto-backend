-- ON CONFLICT DO NOTHING makes materialising the suggested plans safe to run twice, and
-- makes a client retry idempotent.
-- name: CreateMaintenancePlan :one
INSERT INTO maintenance_plans (
    id, vehicle_id, maintenance_item_id,
    interval_km, interval_months, interval_days,
    alert_km, alert_days, origin
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (vehicle_id, maintenance_item_id) DO NOTHING
RETURNING *;

-- The item columns are joined in because every consumer needs them: the due engine wants
-- the name for ordering, and the app wants it for display.
-- name: ListMaintenancePlansForVehicle :many
SELECT p.*, i.slug AS item_slug, i.name AS item_name, i.kind AS item_kind
FROM maintenance_plans p
JOIN maintenance_items i ON i.id = p.maintenance_item_id
WHERE p.vehicle_id = $1
  AND p.is_active
ORDER BY i.kind, i.name;

-- name: GetMaintenancePlan :one
SELECT p.*, i.slug AS item_slug, i.name AS item_name, i.kind AS item_kind
FROM maintenance_plans p
JOIN maintenance_items i ON i.id = p.maintenance_item_id
WHERE p.id = $1;

-- PATCH semantics: a NULL argument leaves the column alone.
--
-- Clearing an interval back to "no periodicity" therefore needs its own flag rather than
-- a null, because null already means "unchanged" here.
-- name: UpdateMaintenancePlan :one
UPDATE maintenance_plans
SET interval_km     = CASE WHEN sqlc.arg('clear_intervals')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('interval_km'), interval_km) END,
    interval_months = CASE WHEN sqlc.arg('clear_intervals')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('interval_months'), interval_months) END,
    interval_days   = CASE WHEN sqlc.arg('clear_intervals')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('interval_days'), interval_days) END,
    alert_km        = COALESCE(sqlc.narg('alert_km'), alert_km),
    alert_days      = COALESCE(sqlc.narg('alert_days'), alert_days),
    -- Any edit makes the plan the owner's, so a future refresh of the suggested defaults
    -- will not overwrite it.
    origin          = 'user',
    updated_at      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- Soft-disable rather than delete: the plan may already have history attached, and the
-- owner may want it back.
-- name: DeactivateMaintenancePlan :execrows
UPDATE maintenance_plans
SET is_active = false, updated_at = now()
WHERE id = $1 AND is_active;
