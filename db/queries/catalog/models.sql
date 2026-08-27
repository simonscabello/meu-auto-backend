-- name: ListVehicleModels :many
SELECT *
FROM vehicle_models
WHERE brand_id = $1
ORDER BY name;

-- The model plus the brand it belongs to, in one read.
--
-- Every model-scoped request needs both: the brand carries the vehicle_type and the
-- provider's brand id, and the provider's URL for "the years of this model" needs both
-- ids. Fetching them separately would be two round trips for one fact.
-- name: GetVehicleModelWithBrand :one
SELECT sqlc.embed(m), sqlc.embed(b)
FROM vehicle_models m
JOIN vehicle_brands b ON b.id = m.brand_id
WHERE m.id = $1;

-- name: UpsertVehicleModels :exec
INSERT INTO vehicle_models (brand_id, external_id, name)
SELECT sqlc.arg('brand_id'), e.external_id, n.name
FROM unnest(sqlc.arg('external_ids')::text[]) WITH ORDINALITY AS e (external_id, idx)
JOIN unnest(sqlc.arg('names')::text[])        WITH ORDINALITY AS n (name, idx)
  USING (idx)
ON CONFLICT (brand_id, external_id)
DO UPDATE SET name       = EXCLUDED.name,
              synced_at  = now(),
              updated_at = now();

-- name: MarkVehicleModelYearsSynced :exec
UPDATE vehicle_models
SET years_synced_at = now(),
    updated_at      = now()
WHERE id = $1;
