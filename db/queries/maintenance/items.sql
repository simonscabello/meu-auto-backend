-- The catalogue a user can see: every global entry, plus their own custom items.
-- name: ListMaintenanceItems :many
SELECT *
FROM maintenance_items
WHERE is_active
  AND (owner_user_id IS NULL OR owner_user_id = sqlc.arg('user_id'))
  AND (vehicle_type = 'all' OR vehicle_type = sqlc.arg('vehicle_type'))
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
ORDER BY kind, name;

-- Scoped the same way as the list: a user must not be able to reference somebody else's
-- custom item by id.
-- name: GetMaintenanceItem :one
SELECT *
FROM maintenance_items
WHERE id = sqlc.arg('id')
  AND is_active
  AND (owner_user_id IS NULL OR owner_user_id = sqlc.arg('user_id'));

-- name: CreateCustomMaintenanceItem :one
INSERT INTO maintenance_items (
    id, slug, name, kind, vehicle_type, owner_user_id,
    default_interval_km, default_interval_months, default_interval_days,
    suggest_by_default
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, false)
ON CONFLICT (owner_user_id, slug) WHERE owner_user_id IS NOT NULL DO NOTHING
RETURNING *;

-- The entries a new vehicle gets a plan for (SPEC.md RN-09).
-- name: ListSuggestedMaintenanceItems :many
SELECT *
FROM maintenance_items
WHERE owner_user_id IS NULL
  AND is_active
  AND suggest_by_default
  AND (vehicle_type = 'all' OR vehicle_type = $1)
ORDER BY kind, name;
